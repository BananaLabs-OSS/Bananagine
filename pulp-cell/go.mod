module bananagine-cell

go 1.25.13

require (
	github.com/BananaLabs-OSS/Fiber v0.0.0
	github.com/MonkeyLabs-LLC/Marrow v0.1.1
	github.com/bananalabs-oss/bananagine v0.0.0
	github.com/vmihailenco/msgpack/v5 v5.4.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
)

replace github.com/BananaLabs-OSS/Fiber => ../../Fiber

replace github.com/MonkeyLabs-LLC/Marrow => ../../Marrow

replace github.com/bananalabs-oss/bananagine => ..
