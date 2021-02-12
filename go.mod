module github.com/snapcore/snapd

go 1.15

replace github.com/pilebones/go-udev => ./osutil/udev

require (
	github.com/boltdb/bolt v1.3.1 // indirect
	github.com/canonical/go-tpm2 v0.0.0-20210208190529-13cc6a20780b
	github.com/canonical/tcglog-parser v0.0.0-20201119144449-21b395fa8a57 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf
	github.com/godbus/dbus v4.1.0+incompatible
	github.com/gorilla/mux v1.8.0
	github.com/jessevdk/go-flags v1.4.0
	github.com/juju/ratelimit v1.0.1
	github.com/mvo5/goconfigparser v0.0.0-20201015074339-50f22f44deb5
	github.com/mvo5/libseccomp-golang v0.9.0
	github.com/pilebones/go-udev v0.0.0-00010101000000-000000000000
	github.com/snapcore/bolt v1.3.1
	github.com/snapcore/go-gettext v0.0.0-20201130093759-38740d1bd3d2
	github.com/snapcore/secboot v0.0.0-20201119144827-cb0d627c6362
	github.com/snapcore/squashfuse v0.0.0-20171220165323-319f6d41a041
	go.mozilla.org/pkcs7 v0.0.0-20200128120323-432b2356ecb1 // indirect
	golang.org/x/crypto v0.0.0-20201221181555-eec23a3978ad
	golang.org/x/sys v0.0.0-20210124154548-22da62e12c0c
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c
	gopkg.in/macaroon.v1 v1.0.0
	gopkg.in/mgo.v2 v2.0.0-20190816093944-a6b53ec6cb22
	gopkg.in/retry.v1 v1.0.3
	gopkg.in/tomb.v2 v2.0.0-20161208151619-d5d1b5820637
	gopkg.in/yaml.v2 v2.4.0
	maze.io/x/crypto v0.0.0-20190131090603-9b94c9afe066 // indirect
)
