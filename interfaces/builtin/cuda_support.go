package builtin

const cudaAppArmor = `
# Description: CUDA requires being able to read/write to some nvidia devices

@{PROC}/sys/vm/mmap_min_addr r,
@{PROC}/devices r,
@{PROC}/modules r,
@{PROC}/driver/nvidia/params r,
/sys/devices/system/memory/block_size_bytes r,
unix (bind,listen) type=seqpacket addr="@cuda-uvmfd-[0-9a-f]*",
/dev/nvidia-uvm wr,
/dev/nvidiactl wr,
/{dev,run}/shm/cuda.* rw,
`

const cudaControlSummary = `allows access to NVIDIA devices using CUDA`

func init() {
	registerIface(&commonInterface{
		name:                  "cuda-support",
		summary:               cudaControlSummary,
		connectedPlugAppArmor: cudaAppArmor,
		implicitOnClassic:     true,
		reservedForOS:         true,
	})
}
