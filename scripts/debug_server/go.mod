module github.com/qinyilin/go-agent/scripts/debug_server

go 1.22

require (
	github.com/gin-gonic/gin v1.10.0
	github.com/gorilla/websocket v1.5.1
)

replace github.com/gin-gonic/gin => ./third_party/gin_stub
replace github.com/gorilla/websocket => ./third_party/websocket_stub

