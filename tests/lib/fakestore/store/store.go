// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2016-2020 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package store

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/tylerb/graceful.v1"

	"github.com/snapcore/snapd/asserts"
	"github.com/snapcore/snapd/asserts/sysdb"
	"github.com/snapcore/snapd/asserts/systestkeys"
	"github.com/snapcore/snapd/logger"
	"github.com/snapcore/snapd/snap"
	"github.com/snapcore/snapd/snap/snapfile"
	"github.com/snapcore/snapd/snapdenv"
	"github.com/snapcore/snapd/store"
)

func rootEndpoint(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(418)
	fmt.Fprintf(w, "I'm a teapot")
}

func hexify(in string) string {
	bs, err := base64.RawURLEncoding.DecodeString(in)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", bs)
}

// Store is our snappy software store implementation
type Store struct {
	url       string
	blobDir   string
	assertDir string

	assertFallback bool
	fallback       *store.Store

	srv *graceful.Server
}

type wrappedWriter struct {
	w          http.ResponseWriter
	status     int
	respBuffer *bytes.Buffer
}

func (w *wrappedWriter) Header() http.Header {
	return w.w.Header()
}

func (w *wrappedWriter) Write(bs []byte) (int, error) {
	// save a copy
	w.respBuffer.Write(bs)
	// pass it through
	return w.w.Write(bs)
}

func (w *wrappedWriter) WriteHeader(status int) {
	// save a copy
	w.status = status
	// pass it through
	w.w.WriteHeader(status)
}

func (w *wrappedWriter) Flush() {
	if f, ok := w.w.(http.Flusher); ok {
		f.Flush()
	}
}

func debugLogger(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := &wrappedWriter{
			w:          w,
			respBuffer: &bytes.Buffer{},
		}
		t0 := time.Now()
		// read / copy the body
		// bodyCop := &bytes.Buffer{}
		// io.MultiWriter()
		handler.ServeHTTP(ww, r)
		t := time.Now().Sub(t0)
		// logger.Debugf("REQUEST: %s %s %s %s %s", r.RemoteAddr, r.Method, r.URL, t0)
		logger.Debugf("RESPONSE: %s %s %s %s (@ %s) %d %s", r.RemoteAddr, r.Method, r.URL, t, t0, ww.status, ww.respBuffer.String())
	})
}

// NewStore creates a new store server serving snaps from the given top directory and assertions from topDir/asserts. If assertFallback is true missing assertions are looked up in the main online store.
func NewStore(topDir, addr string, assertFallback bool) *Store {
	mux := http.NewServeMux()
	var sto *store.Store
	if assertFallback {
		snapdenv.SetUserAgentFromVersion("unknown", nil, "fakestore")
		sto = store.New(nil, nil)
	}
	store := &Store{
		blobDir:   topDir,
		assertDir: filepath.Join(topDir, "asserts"),

		assertFallback: assertFallback,
		fallback:       sto,

		url: fmt.Sprintf("http://%s", addr),
		srv: &graceful.Server{
			Timeout: 2 * time.Second,

			Server: &http.Server{
				Addr:    addr,
				Handler: mux,
			},
		},
	}

	mux.Handle("/", debugLogger(http.HandlerFunc(rootEndpoint)))
	mux.Handle("/api/v1/snaps/search", debugLogger(http.HandlerFunc(store.searchEndpoint)))
	mux.Handle("/api/v1/snaps/details/", debugLogger(http.HandlerFunc(store.detailsEndpoint)))
	mux.Handle("/api/v1/snaps/metadata", debugLogger(http.HandlerFunc(store.bulkEndpoint)))
	mux.Handle("/api/v1/snaps/assertions/", debugLogger(http.HandlerFunc(store.assertionsEndpoint)))
	mux.Handle("/api/v1/snaps/auth/nonces", debugLogger(http.HandlerFunc(store.authNonceEndpoint)))
	mux.Handle("/api/v1/snaps/auth/sessions", debugLogger(http.HandlerFunc(store.authSessionsEndpoint)))

	// don't log download it's too verbose
	mux.Handle("/download/", http.StripPrefix("/download/", http.FileServer(http.Dir(topDir))))

	// v2
	mux.Handle("/v2/snaps/refresh", debugLogger(http.HandlerFunc(store.snapActionEndpoint)))

	return store
}

// URL returns the base-url that the store is listening on
func (s *Store) URL() string {
	return s.url
}

func (s *Store) SnapsDir() string {
	return s.blobDir
}

// Start listening
func (s *Store) Start() error {
	l, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return err
	}

	go s.srv.Serve(l)
	return nil
}

// Stop stops the server
func (s *Store) Stop() error {
	timeoutTime := 2000 * time.Millisecond
	s.srv.Stop(timeoutTime / 2)

	select {
	case <-s.srv.StopChan():
	case <-time.After(timeoutTime):
		return fmt.Errorf("store failed to stop after %s", timeoutTime)
	}

	return nil
}

var (
	defaultDeveloper   = "canonical"
	defaultDeveloperID = "canonical"
	defaultRevision    = 424242
)

func makeRevision(info *snap.Info) int {
	// TODO: This is a hack to ensure we have higher
	//       revisions here than locally. The fake
	//       snaps get versions like
	//          "1.0+fake1+fake1+fake1"
	//       so we can use this for now to generate
	//       fake revisions. However in the longer
	//       term we should read the real revision
	//       of the snap, increment and add a ".aux"
	//       file to the download directory of the
	//       store that contains the revision and the
	//       developer. The fake-store can then read
	//       that file when sending the reply.
	n := strings.Count(info.Version, "+fake") + 1
	return n * defaultRevision
}

type essentialInfo struct {
	Name        string
	SnapID      string
	DeveloperID string
	DevelName   string
	Revision    int
	Version     string
	Size        uint64
	Digest      string
	Confinement string
	Type        string
}

var errInfo = errors.New("cannot get info")

func snapEssentialInfo(w http.ResponseWriter, fn, snapID string, bs asserts.Backstore) (*essentialInfo, error) {
	f, err := snapfile.Open(fn)
	if err != nil {
		http.Error(w, fmt.Sprintf("cannot read: %v: %v", fn, err), 400)
		return nil, errInfo
	}

	restoreSanitize := snap.MockSanitizePlugsSlots(func(snapInfo *snap.Info) {})
	defer restoreSanitize()

	info, err := snap.ReadInfoFromSnapFile(f, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("cannot get info for: %v: %v", fn, err), 400)
		return nil, errInfo
	}

	snapDigest, size, err := asserts.SnapFileSHA3_384(fn)
	if err != nil {
		http.Error(w, fmt.Sprintf("cannot get digest for: %v: %v", fn, err), 400)
		return nil, errInfo
	}

	fmt.Println("digest of snapfile is", snapDigest)

	snapRev, devAcct, err := findSnapRevision(snapDigest, bs)
	if err != nil && !asserts.IsNotFound(err) {
		http.Error(w, fmt.Sprintf("cannot get info for: %v: %v", fn, err), 400)
		return nil, errInfo
	}

	var devel, develID string
	var revision int
	if snapRev != nil {
		fmt.Println("snapRev found was", snapRev.SnapRevision())

		snapID = snapRev.SnapID()
		develID = snapRev.DeveloperID()
		devel = devAcct.Username()
		revision = snapRev.SnapRevision()
	} else {
		fmt.Println("snapRev is nil")
		// XXX: fallback until we are always assertion based
		develID = defaultDeveloperID
		devel = defaultDeveloper
		revision = makeRevision(info)
	}

	return &essentialInfo{
		Name:        info.SnapName(),
		SnapID:      snapID,
		DeveloperID: develID,
		DevelName:   devel,
		Revision:    revision,
		Version:     info.Version,
		Digest:      snapDigest,
		Size:        size,
		Confinement: string(info.Confinement),
		Type:        string(info.Type()),
	}, nil
}

type searchPayloadJSON struct {
	Packages []detailsReplyJSON `json:"clickindex:package"`
}

type detailsReplyJSON struct {
	Architectures   []string `json:"architecture"`
	SnapID          string   `json:"snap_id"`
	PackageName     string   `json:"package_name"`
	Developer       string   `json:"origin"`
	DeveloperID     string   `json:"developer_id"`
	AnonDownloadURL string   `json:"anon_download_url"`
	DownloadURL     string   `json:"download_url"`
	Version         string   `json:"version"`
	Revision        int      `json:"revision"`
	DownloadDigest  string   `json:"download_sha3_384"`
	Confinement     string   `json:"confinement"`
	Type            string   `json:"type"`
}

type NonceResponse struct {
	Nonce string `json:"nonce"`
}

func (s *Store) authNonceEndpoint(w http.ResponseWriter, req *http.Request) {
	resp := &NonceResponse{Nonce: "hello-there"}
	b, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error marshalling nonce json: %v", err), 500)
		return
	}

	w.Write(b)
}

type AuthSessionResponse struct {
	Macaroon string `json:"macaroon"`
}

func (s *Store) authSessionsEndpoint(w http.ResponseWriter, req *http.Request) {
	resp := &AuthSessionResponse{Macaroon: "general-kenobi"}
	b, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error marshalling session json: %v", err), 500)
		return
	}

	w.Write(b)
}

func (s *Store) searchEndpoint(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(501)
	fmt.Fprintf(w, "search not implemented")
}

func (s *Store) detailsEndpoint(w http.ResponseWriter, req *http.Request) {
	pkg := strings.TrimPrefix(req.URL.Path, "/api/v1/snaps/details/")
	if pkg == req.URL.Path {
		panic("how?")
	}

	bs, err := s.collectAssertions()
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error collecting assertions: %v", err), 500)
		return
	}
	snaps, err := s.collectSnaps()
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error collecting snaps: %v", err), 500)
		return
	}

	// TODO: support revisions too
	fn, ok := snaps[uniqueSnap{name: pkg}]
	if !ok {
		http.NotFound(w, req)
		return
	}

	essInfo, err := snapEssentialInfo(w, fn, "", bs)
	if essInfo == nil {
		if err != errInfo {
			panic(err)
		}
		return
	}

	details := detailsReplyJSON{
		Architectures:   []string{"all"},
		SnapID:          essInfo.SnapID,
		PackageName:     essInfo.Name,
		Developer:       essInfo.DevelName,
		DeveloperID:     essInfo.DeveloperID,
		AnonDownloadURL: fmt.Sprintf("%s/download/%s", s.URL(), filepath.Base(fn)),
		DownloadURL:     fmt.Sprintf("%s/download/%s", s.URL(), filepath.Base(fn)),
		Version:         essInfo.Version,
		Revision:        essInfo.Revision,
		DownloadDigest:  hexify(essInfo.Digest),
		Confinement:     essInfo.Confinement,
		Type:            essInfo.Type,
	}

	// use indent because this is a development tool, output
	// should look nice
	out, err := json.MarshalIndent(details, "", "    ")
	if err != nil {
		http.Error(w, fmt.Sprintf("cannot marshal: %v: %v", details, err), 400)
		return
	}
	w.Write(out)
}

type uniqueSnap struct {
	revision int
	name     string
}

func (s *Store) collectSnaps() (map[uniqueSnap]string, error) {
	snapFns, err := filepath.Glob(filepath.Join(s.blobDir, "*.snap"))
	if err != nil {
		return nil, err
	}

	snaps := map[uniqueSnap]string{}

	restoreSanitize := snap.MockSanitizePlugsSlots(func(snapInfo *snap.Info) {})
	defer restoreSanitize()

	for _, fn := range snapFns {
		f, err := snapfile.Open(fn)
		if err != nil {
			return nil, err
		}
		info, err := snap.ReadInfoFromSnapFile(f, nil)
		if err != nil {
			return nil, err
		}

		snapPlaceInfo, err := snap.ParsePlaceInfoFromSnapFileName(fn)
		if err != nil {
			// then the filename is just a "core.snap" or something, pretend it's
			// revision 0 or unset
			snaps[uniqueSnap{name: info.SnapName()}] = fn
		} else {
			snaps[uniqueSnap{
				name:     snapPlaceInfo.SnapName(),
				revision: snapPlaceInfo.SnapRevision().N,
			}] = fn
		}

	}

	return snaps, err
}

type candidateSnap struct {
	SnapID string `json:"snap_id"`
}

type bulkReqJSON struct {
	CandidateSnaps []candidateSnap `json:"snaps"`
	Fields         []string        `json:"fields"`
}

type payload struct {
	Packages []detailsReplyJSON `json:"clickindex:package"`
}

type bulkReplyJSON struct {
	Payload payload `json:"_embedded"`
}

var someSnapIDtoName = map[string]map[string]string{
	"production": {
		"b8X2psL1ryVrPt5WEmpYiqfr5emixTd7": "ubuntu-core",
		"99T7MUlRhtI3U0QFgl5mXXESAiSwt776": "core",
		"bul8uZn9U3Ll4ke6BMqvNVEZjuJCSQvO": "canonical-pc",
		"SkKeDk2PRgBrX89DdgULk3pyY5DJo6Jk": "canonical-pc-linux",
		"eFe8BTR5L5V9F7yHeMAPxkEr2NdUXMtw": "test-snapd-tools",
		"Wcs8QL2iRQMjsPYQ4qz4V1uOlElZ1ZOb": "test-snapd-python-webserver",
		"DVvhXhpa9oJjcm0rnxfxftH1oo5vTW1M": "test-snapd-go-webserver",
	},
	"staging": {
		"xMNMpEm0COPZy7jq9YRwWVLCD9q5peow": "core",
		"02AHdOomTzby7gTaiLX3M3SGMmXDfLJp": "test-snapd-tools",
		"uHjTANBWSXSiYzNOUXZNDnOSH3POSqWS": "test-snapd-python-webserver",
		"edmdK5G9fP1q1bGyrjnaDXS4RkdjiTGV": "test-snapd-go-webserver",
	},
}

func (s *Store) bulkEndpoint(w http.ResponseWriter, req *http.Request) {
	var pkgs bulkReqJSON
	var replyData bulkReplyJSON

	decoder := json.NewDecoder(req.Body)
	if err := decoder.Decode(&pkgs); err != nil {
		http.Error(w, fmt.Sprintf("cannot decode request body: %v", err), 400)
		return
	}

	bs, err := s.collectAssertions()
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error collecting assertions: %v", err), 500)
		return
	}

	var remoteStore string
	if snapdenv.UseStagingStore() {
		remoteStore = "staging"
	} else {
		remoteStore = "production"
	}
	snapIDtoName, err := addSnapIDs(bs, someSnapIDtoName[remoteStore])
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error collecting snapIDs: %v", err), 500)
		return
	}

	snaps, err := s.collectSnaps()
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error collecting snaps: %v", err), 500)
		return
	}

	// check if we have downloadable snap of the given SnapID
	for _, pkg := range pkgs.CandidateSnaps {

		name := snapIDtoName[pkg.SnapID]
		if name == "" {
			http.Error(w, fmt.Sprintf("unknown snap-id: %q", pkg.SnapID), 400)
			return
		}

		// TODO: support snap revisions here too
		if fn, ok := snaps[uniqueSnap{name: name}]; ok {
			essInfo, err := snapEssentialInfo(w, fn, pkg.SnapID, bs)
			if essInfo == nil {
				if err != errInfo {
					panic(err)
				}
				return
			}

			replyData.Payload.Packages = append(replyData.Payload.Packages, detailsReplyJSON{
				Architectures:   []string{"all"},
				SnapID:          essInfo.SnapID,
				PackageName:     essInfo.Name,
				Developer:       essInfo.DevelName,
				DeveloperID:     essInfo.DeveloperID,
				DownloadURL:     fmt.Sprintf("%s/download/%s", s.URL(), filepath.Base(fn)),
				AnonDownloadURL: fmt.Sprintf("%s/download/%s", s.URL(), filepath.Base(fn)),
				Version:         essInfo.Version,
				Revision:        essInfo.Revision,
				DownloadDigest:  hexify(essInfo.Digest),
				Confinement:     essInfo.Confinement,
				Type:            essInfo.Type,
			})
		}
	}

	// use indent because this is a development tool, output
	// should look nice
	out, err := json.MarshalIndent(replyData, "", "    ")
	if err != nil {
		http.Error(w, fmt.Sprintf("cannot marshal: %v: %v", replyData, err), 400)
		return
	}
	w.Write(out)

}

func (s *Store) collectAssertions() (asserts.Backstore, error) {
	bs := asserts.NewMemoryBackstore()

	add := func(a asserts.Assertion) {
		bs.Put(a.Type(), a)
	}

	for _, t := range sysdb.Trusted() {
		add(t)
	}
	add(systestkeys.TestRootAccount)
	add(systestkeys.TestRootAccountKey)
	add(systestkeys.TestStoreAccountKey)

	aFiles, err := filepath.Glob(filepath.Join(s.assertDir, "*"))
	if err != nil {
		return nil, err
	}

	for _, fn := range aFiles {
		b, err := ioutil.ReadFile(fn)
		if err != nil {
			return nil, err
		}

		a, err := asserts.Decode(b)
		if err != nil {
			return nil, err
		}

		add(a)
	}

	return bs, nil
}

type currentSnap struct {
	SnapID      string `json:"snap-id"`
	InstanceKey string `json:"instance-key"`
	Revision    int    `json:"revision"`
}

type snapAction struct {
	Action      string `json:"action"`
	InstanceKey string `json:"instance-key"`
	SnapID      string `json:"snap-id"`
	Name        string `json:"name"`
}

type snapActionRequest struct {
	Context []currentSnap `json:"context"`
	Fields  []string      `json:"fields"`
	Actions []snapAction  `json:"actions"`
}

type snapActionResult struct {
	Result      string          `json:"result"`
	InstanceKey string          `json:"instance-key"`
	SnapID      string          `json:"snap-id"`
	Name        string          `json:"name"`
	Snap        detailsResultV2 `json:"snap"`
}

type snapActionResultList struct {
	Results []*snapActionResult `json:"results"`
}

type detailsResultV2 struct {
	Architectures []string `json:"architectures"`
	SnapID        string   `json:"snap-id"`
	Name          string   `json:"name"`
	Publisher     struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"publisher"`
	Download struct {
		URL      string `json:"url"`
		Sha3_384 string `json:"sha3-384"`
		Size     uint64 `json:"size"`
	} `json:"download"`
	Version     string `json:"version"`
	Revision    int    `json:"revision"`
	Confinement string `json:"confinement"`
	Type        string `json:"type"`
}

func (s *Store) snapActionEndpoint(w http.ResponseWriter, req *http.Request) {
	var reqData snapActionRequest
	var replyData snapActionResultList

	decoder := json.NewDecoder(req.Body)
	if err := decoder.Decode(&reqData); err != nil {
		http.Error(w, fmt.Sprintf("cannot decode request body: %v", err), 400)
		return
	}

	bs, err := s.collectAssertions()
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error collecting assertions: %v", err), 500)
		return
	}

	var remoteStore string
	if snapdenv.UseStagingStore() {
		remoteStore = "staging"
	} else {
		remoteStore = "production"
	}
	snapIDtoName, err := addSnapIDs(bs, someSnapIDtoName[remoteStore])
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error collecting snapIDs: %v", err), 500)
		return
	}

	snaps, err := s.collectSnaps()
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error collecting snaps: %v", err), 500)
		return
	}

	actions := reqData.Actions
	if len(actions) == 1 && actions[0].Action == "refresh-all" {
		actions = make([]snapAction, len(reqData.Context))
		for i, s := range reqData.Context {
			actions[i] = snapAction{
				Action:      "refresh",
				SnapID:      s.SnapID,
				InstanceKey: s.InstanceKey,
			}
		}
	}

	realishStore := false
	switch strings.ToLower(os.Getenv("ACT_LIKE_REAL_STORE")) {
	case "true", "1":
		realishStore = true
	}

	// check if we have downloadable snap of the given SnapID or name
	for _, a := range actions {
		name := a.Name
		snapID := a.SnapID
		if a.Action == "refresh" {
			name = snapIDtoName[snapID]
		}

		if name == "" {
			http.Error(w, fmt.Sprintf("unknown snap-id: %q", snapID), 400)
			return
		}

		if realishStore {
			// the real store does not just serve a "name.snap" file for all
			// refreshes, we need to do a bit more logic to act like a real
			// store for refreshes, we need to have the snapID first of all,
			// then look at what revisions we have for that snapID, then look
			// at the context provided to see what snaps could be refreshed to
			// a newer revision
			if snapID == "" {
				http.Error(w, "missing required snap-id in request", 400)
				return
			}

			foundRevisions := []asserts.Assertion{}
			collector := func(a asserts.Assertion) {
				foundRevisions = append(foundRevisions, a)
			}

			// search for all snap revisions that have matching snap-id
			hint := map[string]string{
				"snap-id": snapID,
			}
			err := bs.Search(asserts.SnapRevisionType, hint, collector, asserts.SnapRevisionType.MaxSupportedFormat())
			if err != nil {
				http.Error(w, fmt.Sprintf("internal error searching assertion backend: %v", err), 500)
				return
			}

			// get the current revision from the context for the refresh action
			currentRevision := 0
			found := false
			for _, snapContext := range reqData.Context {
				if snapContext.SnapID == snapID {
					currentRevision = snapContext.Revision
					found = true
					break
				}
			}
			if !found {
				http.Error(w, fmt.Sprintf("missing context for snap-id %s in request", snapID), 400)
				return
			}

			fmt.Println("current revision for snap", name, "is", currentRevision)

			found = false
			newestRev := &asserts.SnapRevision{}
			for _, revAssert := range foundRevisions {
				snapRevAssert := revAssert.(*asserts.SnapRevision)
				fmt.Println("snap rev consdiering for ", name, "is ", snapRevAssert.SnapRevision())
				if snapRevAssert.SnapRevision() > currentRevision {
					newestRev = snapRevAssert
					found = true
				}
			}
			if found {
				// TODO: maybe instead we should use the sha3-384 eventually
				//       instead of snap files named like core20_12.snap to
				//       identify the snap revision

				fmt.Println("looking for snap with revision", newestRev.SnapRevision())

				if fn, ok := snaps[uniqueSnap{revision: newestRev.SnapRevision(), name: name}]; ok {
					fmt.Println("filename found was", fn)
					essInfo, err := snapEssentialInfo(w, fn, snapID, bs)
					if essInfo == nil {
						if err != errInfo {
							panic(err)
						}
						return
					}

					downloadURL := fmt.Sprintf("http://%s", req.Host)

					fmt.Println("got essInfo for snap", name, essInfo)

					res := &snapActionResult{
						Result:      a.Action,
						InstanceKey: a.InstanceKey,
						SnapID:      essInfo.SnapID,
						Name:        essInfo.Name,
						Snap: detailsResultV2{
							Architectures: []string{"all"},
							SnapID:        essInfo.SnapID,
							Name:          essInfo.Name,
							Version:       essInfo.Version,
							Revision:      essInfo.Revision,
							Confinement:   essInfo.Confinement,
							Type:          essInfo.Type,
						},
					}
					res.Snap.Publisher.ID = essInfo.DeveloperID
					res.Snap.Publisher.Username = essInfo.DevelName
					res.Snap.Download.URL = fmt.Sprintf("%s/download/%s", downloadURL, filepath.Base(fn))
					res.Snap.Download.Sha3_384 = hexify(essInfo.Digest)
					res.Snap.Download.Size = essInfo.Size
					replyData.Results = append(replyData.Results, res)

				} else {
					http.Error(w, fmt.Sprintf("internal error finding snap file for snap %s revision %d", name, newestRev.Revision()), 500)
					return
				}
			} else {
				fmt.Println("no new revisions found for snap", name)
			}
		} else {
			if fn, ok := snaps[uniqueSnap{name: name}]; ok {
				essInfo, err := snapEssentialInfo(w, fn, snapID, bs)
				if essInfo == nil {
					if err != errInfo {
						panic(err)
					}
					return
				}

				res := &snapActionResult{
					Result:      a.Action,
					InstanceKey: a.InstanceKey,
					SnapID:      essInfo.SnapID,
					Name:        essInfo.Name,
					Snap: detailsResultV2{
						Architectures: []string{"all"},
						SnapID:        essInfo.SnapID,
						Name:          essInfo.Name,
						Version:       essInfo.Version,
						Revision:      essInfo.Revision,
						Confinement:   essInfo.Confinement,
						Type:          essInfo.Type,
					},
				}
				res.Snap.Publisher.ID = essInfo.DeveloperID
				res.Snap.Publisher.Username = essInfo.DevelName
				res.Snap.Download.URL = fmt.Sprintf("%s/download/%s", s.URL(), filepath.Base(fn))
				res.Snap.Download.Sha3_384 = hexify(essInfo.Digest)
				res.Snap.Download.Size = essInfo.Size
				replyData.Results = append(replyData.Results, res)
			}
		}
	}

	// use indent because this is a development tool, output
	// should look nice
	out, err := json.MarshalIndent(replyData, "", "    ")
	if err != nil {
		http.Error(w, fmt.Sprintf("cannot marshal: %v: %v", replyData, err), 400)
		return
	}
	w.Write(out)
}

func (s *Store) retrieveAssertion(bs asserts.Backstore, assertType *asserts.AssertionType, primaryKey []string) (asserts.Assertion, error) {
	a, err := bs.Get(assertType, primaryKey, assertType.MaxSupportedFormat())
	if asserts.IsNotFound(err) && s.assertFallback {
		return s.fallback.Assertion(assertType, primaryKey, nil)
	}
	return a, err
}

func (s *Store) assertionsEndpoint(w http.ResponseWriter, req *http.Request) {
	assertPath := strings.TrimPrefix(req.URL.Path, "/api/v1/snaps/assertions/")

	bs, err := s.collectAssertions()
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error collecting assertions: %v", err), 500)
		return
	}

	comps := strings.Split(assertPath, "/")

	if len(comps) == 0 {
		http.Error(w, "missing assertion type", 400)
		return

	}

	typ := asserts.Type(comps[0])
	if typ == nil {
		http.Error(w, fmt.Sprintf("unknown assertion type: %s", comps[0]), 400)
		return
	}

	if len(typ.PrimaryKey) != len(comps)-1 {
		http.Error(w, fmt.Sprintf("wrong primary key length: %v", comps), 400)
		return
	}

	a, err := s.retrieveAssertion(bs, typ, comps[1:])
	if asserts.IsNotFound(err) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(404)
		w.Write([]byte(`{"status": 404}`))
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("cannot retrieve assertion %v: %v", comps, err), 400)
		return
	}

	w.Header().Set("Content-Type", asserts.MediaType)
	w.WriteHeader(200)
	w.Write(asserts.Encode(a))
}

func addSnapIDs(bs asserts.Backstore, initial map[string]string) (map[string]string, error) {
	m := make(map[string]string)
	for id, name := range initial {
		m[id] = name
	}

	hit := func(a asserts.Assertion) {
		decl := a.(*asserts.SnapDeclaration)
		m[decl.SnapID()] = decl.SnapName()
	}

	err := bs.Search(asserts.SnapDeclarationType, nil, hit, asserts.SnapDeclarationType.MaxSupportedFormat())
	if err != nil {
		return nil, err
	}

	return m, nil
}

func findSnapRevision(snapDigest string, bs asserts.Backstore) (*asserts.SnapRevision, *asserts.Account, error) {
	a, err := bs.Get(asserts.SnapRevisionType, []string{snapDigest}, asserts.SnapRevisionType.MaxSupportedFormat())
	if err != nil {
		return nil, nil, err
	}
	snapRev := a.(*asserts.SnapRevision)

	a, err = bs.Get(asserts.AccountType, []string{snapRev.DeveloperID()}, asserts.AccountType.MaxSupportedFormat())
	if err != nil {
		return nil, nil, err
	}
	devAcct := a.(*asserts.Account)

	return snapRev, devAcct, nil
}
