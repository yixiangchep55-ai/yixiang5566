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
	Type MsgType `json:"type" mapstructure:"type"`
	Data any     `json:"data" mapstructure:"data"`
}

type VersionPayload struct {
	Version int    `json:"version" mapstructure:"version"`
	Height  uint64 `json:"height" mapstructure:"height"`
	CumWork string `json:"cum_work" mapstructure:"cum_work"`
	NodeID  uint64 `json:"node_id" mapstructure:"node_id"`
}

type InvPayload struct {
	Type   string   `json:"type" mapstructure:"type"`
	Hashes []string `json:"hashes" mapstructure:"hashes"`
}

type GetDataPayload struct {
	Type string `json:"type" mapstructure:"type"`
	Hash string `json:"hash" mapstructure:"hash"`
}

type TxPayload struct {
	Tx []byte `json:"tx" mapstructure:"tx"`
}

type AddrPayload struct {
	Addrs []string `json:"addrs" mapstructure:"addrs"`
}
