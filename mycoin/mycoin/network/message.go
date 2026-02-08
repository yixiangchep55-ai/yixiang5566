package network

type MsgType string

const (
	MsgVersion    MsgType = "version"
	MsgVerAck     MsgType = "verack"
	MsgInv        MsgType = "inv"
	MsgGetData    MsgType = "getdata"
	MsgBlock      MsgType = "block"
	MsgTx         MsgType = "tx"
	MsgAddr       MsgType = "addr"
	MsgGetAddr    MsgType = "getaddr"
	MsgGetHeaders MsgType = "getheaders" // ✅ 新增
	MsgHeaders    MsgType = "headers"    // ✅ 新增
	MsgPing               = "ping"
	MsgPong               = "pong"
)

type Message struct {
	Type MsgType `json:"type"`
	Data any     `json:"data"`
}

type VersionPayload struct {
	Version int    `json:"version"`
	Height  uint64 `json:"height"`
	CumWork string `json:"cum_work"`
	NodeID  string `json:"node_id"`
}

type InvPayload struct {
	Type   string   `json:"type"`   // "block" | "tx"
	Hashes []string `json:"hashes"` // 区块 hash 或 txid
}

// getdata 消息：请求具体数据
type GetDataPayload struct {
	Type string `json:"type"` // "block" | "tx"
	Hash string `json:"hash"`
}

type TxPayload struct {
	Tx []byte `json:"tx"`
}

type AddrPayload struct {
	Addrs []string `json:"addrs"`
}
