module bananagine-state-cell

go 1.25.6

require (
	bananagine-registry-cell v0.0.0
	github.com/BananaLabs-OSS/Fiber v0.0.0
	github.com/bananalabs-oss/bananagine v0.0.0
	github.com/vmihailenco/msgpack/v5 v5.4.1
)

require github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect

replace bananagine-registry-cell => ../registry-cell

replace github.com/BananaLabs-OSS/Fiber => ../../Fiber

replace github.com/bananalabs-oss/bananagine => ..
