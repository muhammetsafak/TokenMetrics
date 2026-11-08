export namespace app {
	
	export class AdminKeyDTO {
	    provider: string;
	    label: string;
	    source: string;
	    configured: boolean;
	    masked: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new AdminKeyDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.label = source["label"];
	        this.source = source["source"];
	        this.configured = source["configured"];
	        this.masked = source["masked"];
	        this.error = source["error"];
	    }
	}
	export class AdminKeysDTO {
	    keys: AdminKeyDTO[];
	    keychainError: string;
	
	    static createFrom(source: any = {}) {
	        return new AdminKeysDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.keys = this.convertValues(source["keys"], AdminKeyDTO);
	        this.keychainError = source["keychainError"];
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
	export class TotalsDTO {
	    records: number;
	    requests: number;
	    inputTokens: number;
	    outputTokens: number;
	    cacheReadTokens: number;
	    cacheWrite5mTokens: number;
	    cacheWrite1hTokens: number;
	    cacheWriteTokens: number;
	    thinkingTokens: number;
	    totalTokens: number;
	    totalOnlyTokens: number;
	    totalOnlyRecords: number;
	    costUsd?: number;
	    costUnavailable: boolean;
	    costPartial: boolean;
	    uncostedTotalOnlyTokens: number;
	    costEstimated: boolean;
	    unknownModels: string[];
	    tierUncertainModels: string[];
	
	    static createFrom(source: any = {}) {
	        return new TotalsDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.records = source["records"];
	        this.requests = source["requests"];
	        this.inputTokens = source["inputTokens"];
	        this.outputTokens = source["outputTokens"];
	        this.cacheReadTokens = source["cacheReadTokens"];
	        this.cacheWrite5mTokens = source["cacheWrite5mTokens"];
	        this.cacheWrite1hTokens = source["cacheWrite1hTokens"];
	        this.cacheWriteTokens = source["cacheWriteTokens"];
	        this.thinkingTokens = source["thinkingTokens"];
	        this.totalTokens = source["totalTokens"];
	        this.totalOnlyTokens = source["totalOnlyTokens"];
	        this.totalOnlyRecords = source["totalOnlyRecords"];
	        this.costUsd = source["costUsd"];
	        this.costUnavailable = source["costUnavailable"];
	        this.costPartial = source["costPartial"];
	        this.uncostedTotalOnlyTokens = source["uncostedTotalOnlyTokens"];
	        this.costEstimated = source["costEstimated"];
	        this.unknownModels = source["unknownModels"];
	        this.tierUncertainModels = source["tierUncertainModels"];
	    }
	}
	export class BreakdownRowDTO {
	    key: string;
	    totals: TotalsDTO;
	
	    static createFrom(source: any = {}) {
	        return new BreakdownRowDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.totals = this.convertValues(source["totals"], TotalsDTO);
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
	export class BreakdownDTO {
	    dimension: string;
	    rows: BreakdownRowDTO[];
	    truncated: boolean;
	    costTruncated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new BreakdownDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dimension = source["dimension"];
	        this.rows = this.convertValues(source["rows"], BreakdownRowDTO);
	        this.truncated = source["truncated"];
	        this.costTruncated = source["costTruncated"];
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
	
	export class BudgetDTO {
	    id: number;
	    scope: string;
	    scopeValue: string;
	    period: string;
	    thresholdTokens?: number;
	    thresholdUsd?: number;
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new BudgetDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.scope = source["scope"];
	        this.scopeValue = source["scopeValue"];
	        this.period = source["period"];
	        this.thresholdTokens = source["thresholdTokens"];
	        this.thresholdUsd = source["thresholdUsd"];
	        this.enabled = source["enabled"];
	    }
	}
	export class CompareRowDTO {
	    service: string;
	    model: string;
	    totals: TotalsDTO;
	
	    static createFrom(source: any = {}) {
	        return new CompareRowDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.service = source["service"];
	        this.model = source["model"];
	        this.totals = this.convertValues(source["totals"], TotalsDTO);
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
	export class CompareDTO {
	    rows: CompareRowDTO[];
	    priceAsOf: string;
	
	    static createFrom(source: any = {}) {
	        return new CompareDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rows = this.convertValues(source["rows"], CompareRowDTO);
	        this.priceAsOf = source["priceAsOf"];
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
	
	export class CurrentWindowDTO {
	    kind: string;
	    startMs: number;
	    endMs: number;
	    totals: TotalsDTO;
	
	    static createFrom(source: any = {}) {
	        return new CurrentWindowDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.startMs = source["startMs"];
	        this.endMs = source["endMs"];
	        this.totals = this.convertValues(source["totals"], TotalsDTO);
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
	export class ExportResultDTO {
	    canceled: boolean;
	    path: string;
	    format: string;
	    records: number;
	
	    static createFrom(source: any = {}) {
	        return new ExportResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.canceled = source["canceled"];
	        this.path = source["path"];
	        this.format = source["format"];
	        this.records = source["records"];
	    }
	}
	export class FilterDTO {
	    fromMs: number;
	    toMs: number;
	    sources: string[];
	    services: string[];
	    models: string[];
	    projects: string[];
	    sessions: string[];
	
	    static createFrom(source: any = {}) {
	        return new FilterDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fromMs = source["fromMs"];
	        this.toMs = source["toMs"];
	        this.sources = source["sources"];
	        this.services = source["services"];
	        this.models = source["models"];
	        this.projects = source["projects"];
	        this.sessions = source["sessions"];
	    }
	}
	export class RowErrorDTO {
	    line: number;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new RowErrorDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.line = source["line"];
	        this.reason = source["reason"];
	    }
	}
	export class ImportResultDTO {
	    canceled: boolean;
	    file: string;
	    format: string;
	    rows: number;
	    added: number;
	    updated: number;
	    unchanged: number;
	    errors: RowErrorDTO[];
	    errorCount: number;
	
	    static createFrom(source: any = {}) {
	        return new ImportResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.canceled = source["canceled"];
	        this.file = source["file"];
	        this.format = source["format"];
	        this.rows = source["rows"];
	        this.added = source["added"];
	        this.updated = source["updated"];
	        this.unchanged = source["unchanged"];
	        this.errors = this.convertValues(source["errors"], RowErrorDTO);
	        this.errorCount = source["errorCount"];
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
	export class LimitDTO {
	    service: string;
	    kind: string;
	    metric: string;
	    used: number;
	    limit: number;
	    ratio: number;
	    windowStartMs: number;
	    windowEndMs: number;
	    estimated: boolean;
	    unknownModels: string[];
	    tierUncertainModels: string[];
	    missingTotalOnlyTokens: number;
	
	    static createFrom(source: any = {}) {
	        return new LimitDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.service = source["service"];
	        this.kind = source["kind"];
	        this.metric = source["metric"];
	        this.used = source["used"];
	        this.limit = source["limit"];
	        this.ratio = source["ratio"];
	        this.windowStartMs = source["windowStartMs"];
	        this.windowEndMs = source["windowEndMs"];
	        this.estimated = source["estimated"];
	        this.unknownModels = source["unknownModels"];
	        this.tierUncertainModels = source["tierUncertainModels"];
	        this.missingTotalOnlyTokens = source["missingTotalOnlyTokens"];
	    }
	}
	export class LimitSettingDTO {
	    id: number;
	    service: string;
	    kind: string;
	    metric: string;
	    limit: number;
	
	    static createFrom(source: any = {}) {
	        return new LimitSettingDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.service = source["service"];
	        this.kind = source["kind"];
	        this.metric = source["metric"];
	        this.limit = source["limit"];
	    }
	}
	export class WeeklyStartDTO {
	    weekday: number;
	    hour: number;
	    timezone: string;
	
	    static createFrom(source: any = {}) {
	        return new WeeklyStartDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.weekday = source["weekday"];
	        this.hour = source["hour"];
	        this.timezone = source["timezone"];
	    }
	}
	export class LimitSettingsDTO {
	    limits: LimitSettingDTO[];
	    weeklyStart: WeeklyStartDTO;
	
	    static createFrom(source: any = {}) {
	        return new LimitSettingsDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.limits = this.convertValues(source["limits"], LimitSettingDTO);
	        this.weeklyStart = this.convertValues(source["weeklyStart"], WeeklyStartDTO);
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
	export class LimitsDTO {
	    limits: LimitDTO[];
	    current: CurrentWindowDTO[];
	    priceAsOf: string;
	    nowMs: number;
	
	    static createFrom(source: any = {}) {
	        return new LimitsDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.limits = this.convertValues(source["limits"], LimitDTO);
	        this.current = this.convertValues(source["current"], CurrentWindowDTO);
	        this.priceAsOf = source["priceAsOf"];
	        this.nowMs = source["nowMs"];
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
	export class ManualRecordDTO {
	    externalId: string;
	    timestampMs: number;
	    service: string;
	    model: string;
	    project: string;
	    session: string;
	    inputTokens: number;
	    outputTokens: number;
	    cacheReadTokens: number;
	    cacheWrite5mTokens: number;
	    cacheWrite1hTokens: number;
	    thinkingTokens: number;
	    requestCount: number;
	    costUsd?: number;
	
	    static createFrom(source: any = {}) {
	        return new ManualRecordDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.externalId = source["externalId"];
	        this.timestampMs = source["timestampMs"];
	        this.service = source["service"];
	        this.model = source["model"];
	        this.project = source["project"];
	        this.session = source["session"];
	        this.inputTokens = source["inputTokens"];
	        this.outputTokens = source["outputTokens"];
	        this.cacheReadTokens = source["cacheReadTokens"];
	        this.cacheWrite5mTokens = source["cacheWrite5mTokens"];
	        this.cacheWrite1hTokens = source["cacheWrite1hTokens"];
	        this.thinkingTokens = source["thinkingTokens"];
	        this.requestCount = source["requestCount"];
	        this.costUsd = source["costUsd"];
	    }
	}
	export class ManualListDTO {
	    rows: ManualRecordDTO[];
	    total: number;
	    truncated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ManualListDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rows = this.convertValues(source["rows"], ManualRecordDTO);
	        this.total = source["total"];
	        this.truncated = source["truncated"];
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
	
	export class ScanStep {
	    scanId: number;
	    source: string;
	    label: string;
	    index: number;
	    total: number;
	    status: string;
	    added: number;
	    updated: number;
	    files: number;
	    warnings: string[];
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new ScanStep(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.scanId = source["scanId"];
	        this.source = source["source"];
	        this.label = source["label"];
	        this.index = source["index"];
	        this.total = source["total"];
	        this.status = source["status"];
	        this.added = source["added"];
	        this.updated = source["updated"];
	        this.files = source["files"];
	        this.warnings = source["warnings"];
	        this.error = source["error"];
	    }
	}
	export class ScanSummaryDTO {
	    id: number;
	    startedMs: number;
	    finishedMs: number;
	    added: number;
	    updated: number;
	    canceled: boolean;
	    steps: ScanStep[];
	
	    static createFrom(source: any = {}) {
	        return new ScanSummaryDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.startedMs = source["startedMs"];
	        this.finishedMs = source["finishedMs"];
	        this.added = source["added"];
	        this.updated = source["updated"];
	        this.canceled = source["canceled"];
	        this.steps = this.convertValues(source["steps"], ScanStep);
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
	export class SourceDTO {
	    id: string;
	    label: string;
	    kind: string;
	    collector: boolean;
	    enabled: boolean;
	    active: boolean;
	    records: number;
	
	    static createFrom(source: any = {}) {
	        return new SourceDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.kind = source["kind"];
	        this.collector = source["collector"];
	        this.enabled = source["enabled"];
	        this.active = source["active"];
	        this.records = source["records"];
	    }
	}
	export class MetaDTO {
	    sources: SourceDTO[];
	    services: string[];
	    models: string[];
	    projects: string[];
	    totalRecords: number;
	    firstRecordMs: number;
	    lastScanMs: number;
	    scanning: boolean;
	    scanId: number;
	    lastScan?: ScanSummaryDTO;
	    priceAsOf: string;
	    timeZone: string;
	
	    static createFrom(source: any = {}) {
	        return new MetaDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sources = this.convertValues(source["sources"], SourceDTO);
	        this.services = source["services"];
	        this.models = source["models"];
	        this.projects = source["projects"];
	        this.totalRecords = source["totalRecords"];
	        this.firstRecordMs = source["firstRecordMs"];
	        this.lastScanMs = source["lastScanMs"];
	        this.scanning = source["scanning"];
	        this.scanId = source["scanId"];
	        this.lastScan = this.convertValues(source["lastScan"], ScanSummaryDTO);
	        this.priceAsOf = source["priceAsOf"];
	        this.timeZone = source["timeZone"];
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
	export class PriceOverrideDTO {
	    model: string;
	    input?: number;
	    output?: number;
	    cacheRead?: number;
	    cacheWrite5m?: number;
	    cacheWrite1h?: number;
	
	    static createFrom(source: any = {}) {
	        return new PriceOverrideDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.input = source["input"];
	        this.output = source["output"];
	        this.cacheRead = source["cacheRead"];
	        this.cacheWrite5m = source["cacheWrite5m"];
	        this.cacheWrite1h = source["cacheWrite1h"];
	    }
	}
	export class PriceSetDTO {
	    input: number;
	    output: number;
	    cacheRead: number;
	    cacheWrite5m: number;
	    cacheWrite1h: number;
	
	    static createFrom(source: any = {}) {
	        return new PriceSetDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.input = source["input"];
	        this.output = source["output"];
	        this.cacheRead = source["cacheRead"];
	        this.cacheWrite5m = source["cacheWrite5m"];
	        this.cacheWrite1h = source["cacheWrite1h"];
	    }
	}
	export class PriceRowDTO {
	    model: string;
	    service: string;
	    source: string;
	    custom: boolean;
	    overridden: boolean;
	    default?: PriceSetDTO;
	    effective?: PriceSetDTO;
	    override?: PriceOverrideDTO;
	    longPrompt?: PriceSetDTO;
	    validUntil: string;
	    expired: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PriceRowDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.service = source["service"];
	        this.source = source["source"];
	        this.custom = source["custom"];
	        this.overridden = source["overridden"];
	        this.default = this.convertValues(source["default"], PriceSetDTO);
	        this.effective = this.convertValues(source["effective"], PriceSetDTO);
	        this.override = this.convertValues(source["override"], PriceOverrideDTO);
	        this.longPrompt = this.convertValues(source["longPrompt"], PriceSetDTO);
	        this.validUntil = source["validUntil"];
	        this.expired = source["expired"];
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
	
	export class PricesDTO {
	    rows: PriceRowDTO[];
	    asOf: string;
	    longPromptThreshold: number;
	
	    static createFrom(source: any = {}) {
	        return new PricesDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rows = this.convertValues(source["rows"], PriceRowDTO);
	        this.asOf = source["asOf"];
	        this.longPromptThreshold = source["longPromptThreshold"];
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
	
	
	
	export class SeriesDTO {
	    key: string;
	    values: number[];
	
	    static createFrom(source: any = {}) {
	        return new SeriesDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.values = source["values"];
	    }
	}
	
	export class SourceSettingDTO {
	    id: string;
	    label: string;
	    kind: string;
	    collector: boolean;
	    enabled: boolean;
	    active: boolean;
	    records: number;
	    provider: string;
	    hasKey: boolean;
	    pathEditable: boolean;
	    pathOverride: string;
	    defaultPath: string;
	    effectivePath: string;
	    pathExists: boolean;
	    lastStatus: string;
	    lastError: string;
	    lastWarnings: string[];
	    lastAdded: number;
	    lastUpdated: number;
	    lastFiles: number;
	    lastScanMs: number;
	
	    static createFrom(source: any = {}) {
	        return new SourceSettingDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.kind = source["kind"];
	        this.collector = source["collector"];
	        this.enabled = source["enabled"];
	        this.active = source["active"];
	        this.records = source["records"];
	        this.provider = source["provider"];
	        this.hasKey = source["hasKey"];
	        this.pathEditable = source["pathEditable"];
	        this.pathOverride = source["pathOverride"];
	        this.defaultPath = source["defaultPath"];
	        this.effectivePath = source["effectivePath"];
	        this.pathExists = source["pathExists"];
	        this.lastStatus = source["lastStatus"];
	        this.lastError = source["lastError"];
	        this.lastWarnings = source["lastWarnings"];
	        this.lastAdded = source["lastAdded"];
	        this.lastUpdated = source["lastUpdated"];
	        this.lastFiles = source["lastFiles"];
	        this.lastScanMs = source["lastScanMs"];
	    }
	}
	export class SummaryDTO {
	    today: TotalsDTO;
	    last7d: TotalsDTO;
	    last30d: TotalsDTO;
	    selected: TotalsDTO;
	    priceAsOf: string;
	    nowMs: number;
	    timeZone: string;
	    dayStartMs: number;
	
	    static createFrom(source: any = {}) {
	        return new SummaryDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.today = this.convertValues(source["today"], TotalsDTO);
	        this.last7d = this.convertValues(source["last7d"], TotalsDTO);
	        this.last30d = this.convertValues(source["last30d"], TotalsDTO);
	        this.selected = this.convertValues(source["selected"], TotalsDTO);
	        this.priceAsOf = source["priceAsOf"];
	        this.nowMs = source["nowMs"];
	        this.timeZone = source["timeZone"];
	        this.dayStartMs = source["dayStartMs"];
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
	export class TimeSeriesDTO {
	    bucket: string;
	    stackBy: string;
	    buckets: number[];
	    series: SeriesDTO[];
	    otherKey: string;
	
	    static createFrom(source: any = {}) {
	        return new TimeSeriesDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bucket = source["bucket"];
	        this.stackBy = source["stackBy"];
	        this.buckets = source["buckets"];
	        this.series = this.convertValues(source["series"], SeriesDTO);
	        this.otherKey = source["otherKey"];
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
	

}

