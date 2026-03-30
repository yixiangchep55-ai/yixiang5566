export namespace main {
	
	export class BackendLaunchResult {
	    started: boolean;
	    alreadyRunning: boolean;
	    mode: string;
	    executable: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new BackendLaunchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.started = source["started"];
	        this.alreadyRunning = source["alreadyRunning"];
	        this.mode = source["mode"];
	        this.executable = source["executable"];
	        this.message = source["message"];
	    }
	}
	export class DashboardEvent {
	    time: string;
	    category: string;
	    title: string;
	    description: string;
	
	    static createFrom(source: any = {}) {
	        return new DashboardEvent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = source["time"];
	        this.category = source["category"];
	        this.title = source["title"];
	        this.description = source["description"];
	    }
	}
	export class IndexerStatusView {
	    enabled: boolean;
	    requested: boolean;
	    reachable: boolean;
	    running: boolean;
	    using_default: boolean;
	    needs_restart: boolean;
	    status: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new IndexerStatusView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.requested = source["requested"];
	        this.reachable = source["reachable"];
	        this.running = source["running"];
	        this.using_default = source["using_default"];
	        this.needs_restart = source["needs_restart"];
	        this.status = source["status"];
	        this.message = source["message"];
	    }
	}
	export class DashboardSettings {
	    apiBase: string;
	    explorerBaseURL: string;
	    enableIndexer: boolean;
	    databaseURL: string;
	
	    static createFrom(source: any = {}) {
	        return new DashboardSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.apiBase = source["apiBase"];
	        this.explorerBaseURL = source["explorerBaseURL"];
	        this.enableIndexer = source["enableIndexer"];
	        this.databaseURL = source["databaseURL"];
	    }
	}
	export class HeightPoint {
	    label: string;
	    height: number;
	
	    static createFrom(source: any = {}) {
	        return new HeightPoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.height = source["height"];
	    }
	}
	export class dashboardWalletActivity {
	    status: string;
	    type: string;
	    amount: number;
	    confirmations: number;
	    txid: string;
	    timestamp: number;
	
	    static createFrom(source: any = {}) {
	        return new dashboardWalletActivity(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.type = source["type"];
	        this.amount = source["amount"];
	        this.confirmations = source["confirmations"];
	        this.txid = source["txid"];
	        this.timestamp = source["timestamp"];
	    }
	}
	export class dashboardWalletView {
	    address: string;
	    balance: number;
	    spendable_balance: number;
	    pending_amount: number;
	    pending_txs: number;
	    activity: dashboardWalletActivity[];
	
	    static createFrom(source: any = {}) {
	        return new dashboardWalletView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.address = source["address"];
	        this.balance = source["balance"];
	        this.spendable_balance = source["spendable_balance"];
	        this.pending_amount = source["pending_amount"];
	        this.pending_txs = source["pending_txs"];
	        this.activity = this.convertValues(source["activity"], dashboardWalletActivity);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class dashboardNodeStatus {
	    node_id: number;
	    mode: string;
	    best_height: number;
	    best_hash: string;
	    synced: boolean;
	    sync_state: string;
	    is_syncing: boolean;
	    peer_count: number;
	    last_peer_event: string;
	    last_peer_addr: string;
	    last_peer_error: string;
	    last_peer_seen_at: string;
	    mempool_count: number;
	    orphan_count: number;
	    mining_enabled: boolean;
	    mining_address: string;
	
	    static createFrom(source: any = {}) {
	        return new dashboardNodeStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.node_id = source["node_id"];
	        this.mode = source["mode"];
	        this.best_height = source["best_height"];
	        this.best_hash = source["best_hash"];
	        this.synced = source["synced"];
	        this.sync_state = source["sync_state"];
	        this.is_syncing = source["is_syncing"];
	        this.peer_count = source["peer_count"];
	        this.last_peer_event = source["last_peer_event"];
	        this.last_peer_addr = source["last_peer_addr"];
	        this.last_peer_error = source["last_peer_error"];
	        this.last_peer_seen_at = source["last_peer_seen_at"];
	        this.mempool_count = source["mempool_count"];
	        this.orphan_count = source["orphan_count"];
	        this.mining_enabled = source["mining_enabled"];
	        this.mining_address = source["mining_address"];
	    }
	}
	export class DashboardPayload {
	    connected: boolean;
	    timestamp: string;
	    node: dashboardNodeStatus;
	    wallet: dashboardWalletView;
	    events: DashboardEvent[];
	    history: HeightPoint[];
	    backend_log: string[];
	    settings: DashboardSettings;
	    indexer: IndexerStatusView;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new DashboardPayload(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.connected = source["connected"];
	        this.timestamp = source["timestamp"];
	        this.node = this.convertValues(source["node"], dashboardNodeStatus);
	        this.wallet = this.convertValues(source["wallet"], dashboardWalletView);
	        this.events = this.convertValues(source["events"], DashboardEvent);
	        this.history = this.convertValues(source["history"], HeightPoint);
	        this.backend_log = source["backend_log"];
	        this.settings = this.convertValues(source["settings"], DashboardSettings);
	        this.indexer = this.convertValues(source["indexer"], IndexerStatusView);
	        this.message = source["message"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	export class MiningControlResult {
	    mining_enabled: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new MiningControlResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mining_enabled = source["mining_enabled"];
	        this.message = source["message"];
	    }
	}
	export class SendTransactionRequest {
	    to: string;
	    amount: number;
	    fee: number;
	
	    static createFrom(source: any = {}) {
	        return new SendTransactionRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.to = source["to"];
	        this.amount = source["amount"];
	        this.fee = source["fee"];
	    }
	}
	export class SendTransactionResult {
	    success: boolean;
	    txid: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new SendTransactionResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.txid = source["txid"];
	        this.message = source["message"];
	    }
	}
	
	

}

