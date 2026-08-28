module runtime-catalog-state-cell

go 1.25.6

require (
	runtime-directory-cell v0.0.0
	github.com/BananaLabs-OSS/Fiber v0.0.0
	github.com/bananalabs-oss/bananagine v0.0.0
	github.com/vmihailenco/msgpack/v5 v5.4.1
)

require github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect

replace runtime-directory-cell => ../registry-cell

replace github.com/BananaLabs-OSS/Fiber => ../../Fiber

replace github.com/bananalabs-oss/bananagine => ..
