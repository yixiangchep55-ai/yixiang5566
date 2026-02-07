package node

type SyncState int

const (
	SyncIdle    SyncState = iota // 什么都没做
	SyncIBD                      // 初始区块下载 Initial Block Download
	SyncHeaders                  // 正在同步 Headers
	SyncBodies                   // 正在同步 Block Bodies
	SyncSynced                   // 全部同步完成
)
