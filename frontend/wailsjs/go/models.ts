export namespace catalog {
	
	export class Asset {
	    id: number;
	    name: string;
	    size: number;
	    contentType: string;
	    downloadUrl: string;
	    apiUrl?: string;
	    digest?: string;
	    downloadCount: number;
	    score?: number;
	    recommended?: boolean;
	    installable: boolean;
	    reason?: string;
	    platform?: string;
	    junk?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Asset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.size = source["size"];
	        this.contentType = source["contentType"];
	        this.downloadUrl = source["downloadUrl"];
	        this.apiUrl = source["apiUrl"];
	        this.digest = source["digest"];
	        this.downloadCount = source["downloadCount"];
	        this.score = source["score"];
	        this.recommended = source["recommended"];
	        this.installable = source["installable"];
	        this.reason = source["reason"];
	        this.platform = source["platform"];
	        this.junk = source["junk"];
	    }
	}
	export class RateLimit {
	    limit: number;
	    remaining: number;
	    // Go type: time
	    reset: any;
	
	    static createFrom(source: any = {}) {
	        return new RateLimit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.limit = source["limit"];
	        this.remaining = source["remaining"];
	        this.reset = this.convertValues(source["reset"], null);
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
	export class Release {
	    id: number;
	    tagName: string;
	    name: string;
	    body: string;
	    draft: boolean;
	    prerelease: boolean;
	    // Go type: time
	    publishedAt: any;
	    // Go type: time
	    createdAt: any;
	    htmlUrl: string;
	    assets: Asset[];
	    recommendedAssetId: number;
	
	    static createFrom(source: any = {}) {
	        return new Release(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.tagName = source["tagName"];
	        this.name = source["name"];
	        this.body = source["body"];
	        this.draft = source["draft"];
	        this.prerelease = source["prerelease"];
	        this.publishedAt = this.convertValues(source["publishedAt"], null);
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.htmlUrl = source["htmlUrl"];
	        this.assets = this.convertValues(source["assets"], Asset);
	        this.recommendedAssetId = source["recommendedAssetId"];
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
	export class RepoInfo {
	    owner: string;
	    name: string;
	    fullName: string;
	    description: string;
	    stars: number;
	    forks: number;
	    openIssues: number;
	    defaultBranch: string;
	    avatarUrl: string;
	    htmlUrl: string;
	    topics: string[];
	    license: string;
	    archived: boolean;
	    // Go type: time
	    updatedAt: any;
	    sourceHost?: string;
	    language?: string;
	    latestReleaseTag?: string;
	    // Go type: time
	    latestReleaseAt?: any;
	    downloadCount?: number;
	    dailyStars?: number;
	    trendingScore?: number;
	    popularityScore?: number;
	    topicCodes?: string[];
	    platformReady: boolean;
	    platformNote?: string;
	    platforms?: string[];
	
	    static createFrom(source: any = {}) {
	        return new RepoInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.owner = source["owner"];
	        this.name = source["name"];
	        this.fullName = source["fullName"];
	        this.description = source["description"];
	        this.stars = source["stars"];
	        this.forks = source["forks"];
	        this.openIssues = source["openIssues"];
	        this.defaultBranch = source["defaultBranch"];
	        this.avatarUrl = source["avatarUrl"];
	        this.htmlUrl = source["htmlUrl"];
	        this.topics = source["topics"];
	        this.license = source["license"];
	        this.archived = source["archived"];
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
	        this.sourceHost = source["sourceHost"];
	        this.language = source["language"];
	        this.latestReleaseTag = source["latestReleaseTag"];
	        this.latestReleaseAt = this.convertValues(source["latestReleaseAt"], null);
	        this.downloadCount = source["downloadCount"];
	        this.dailyStars = source["dailyStars"];
	        this.trendingScore = source["trendingScore"];
	        this.popularityScore = source["popularityScore"];
	        this.topicCodes = source["topicCodes"];
	        this.platformReady = source["platformReady"];
	        this.platformNote = source["platformNote"];
	        this.platforms = source["platforms"];
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
	export class Target {
	    os: string;
	    arch: string;
	
	    static createFrom(source: any = {}) {
	        return new Target(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.os = source["os"];
	        this.arch = source["arch"];
	    }
	}
	export class RepoView {
	    owner: string;
	    name: string;
	    fullName: string;
	    description: string;
	    stars: number;
	    forks: number;
	    openIssues: number;
	    defaultBranch: string;
	    avatarUrl: string;
	    htmlUrl: string;
	    topics: string[];
	    license: string;
	    archived: boolean;
	    // Go type: time
	    updatedAt: any;
	    sourceHost?: string;
	    language?: string;
	    latestReleaseTag?: string;
	    // Go type: time
	    latestReleaseAt?: any;
	    downloadCount?: number;
	    dailyStars?: number;
	    trendingScore?: number;
	    popularityScore?: number;
	    topicCodes?: string[];
	    platformReady: boolean;
	    platformNote?: string;
	    platforms?: string[];
	    target: Target;
	    releases: Release[];
	    stale: boolean;
	    // Go type: time
	    cachedAt: any;
	    rateLimit: RateLimit;
	    readme: string;
	    readmeStale: boolean;
	    // Go type: time
	    readmeCachedAt: any;
	    readmeError?: string;
	    issuesUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new RepoView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.owner = source["owner"];
	        this.name = source["name"];
	        this.fullName = source["fullName"];
	        this.description = source["description"];
	        this.stars = source["stars"];
	        this.forks = source["forks"];
	        this.openIssues = source["openIssues"];
	        this.defaultBranch = source["defaultBranch"];
	        this.avatarUrl = source["avatarUrl"];
	        this.htmlUrl = source["htmlUrl"];
	        this.topics = source["topics"];
	        this.license = source["license"];
	        this.archived = source["archived"];
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
	        this.sourceHost = source["sourceHost"];
	        this.language = source["language"];
	        this.latestReleaseTag = source["latestReleaseTag"];
	        this.latestReleaseAt = this.convertValues(source["latestReleaseAt"], null);
	        this.downloadCount = source["downloadCount"];
	        this.dailyStars = source["dailyStars"];
	        this.trendingScore = source["trendingScore"];
	        this.popularityScore = source["popularityScore"];
	        this.topicCodes = source["topicCodes"];
	        this.platformReady = source["platformReady"];
	        this.platformNote = source["platformNote"];
	        this.platforms = source["platforms"];
	        this.target = this.convertValues(source["target"], Target);
	        this.releases = this.convertValues(source["releases"], Release);
	        this.stale = source["stale"];
	        this.cachedAt = this.convertValues(source["cachedAt"], null);
	        this.rateLimit = this.convertValues(source["rateLimit"], RateLimit);
	        this.readme = source["readme"];
	        this.readmeStale = source["readmeStale"];
	        this.readmeCachedAt = this.convertValues(source["readmeCachedAt"], null);
	        this.readmeError = source["readmeError"];
	        this.issuesUrl = source["issuesUrl"];
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

export namespace download {
	
	export class Job {
	    id: string;
	    repoKey: string;
	    displayName: string;
	    assetName: string;
	    assetId: number;
	    assetUrl: string;
	    digest: string;
	    tag: string;
	    publishedAt: number;
	    status: string;
	    error: string;
	    destPath: string;
	    total: number;
	    done: number;
	    speed: number;
	    percent: number;
	    version: string;
	    verified: boolean;
	    source: string;
	    canResume: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Job(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.repoKey = source["repoKey"];
	        this.displayName = source["displayName"];
	        this.assetName = source["assetName"];
	        this.assetId = source["assetId"];
	        this.assetUrl = source["assetUrl"];
	        this.digest = source["digest"];
	        this.tag = source["tag"];
	        this.publishedAt = source["publishedAt"];
	        this.status = source["status"];
	        this.error = source["error"];
	        this.destPath = source["destPath"];
	        this.total = source["total"];
	        this.done = source["done"];
	        this.speed = source["speed"];
	        this.percent = source["percent"];
	        this.version = source["version"];
	        this.verified = source["verified"];
	        this.source = source["source"];
	        this.canResume = source["canResume"];
	    }
	}
	export class Mirror {
	    name: string;
	    template: string;
	    api?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Mirror(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.template = source["template"];
	        this.api = source["api"];
	    }
	}
	export class MirrorResult {
	    name: string;
	    template: string;
	    latencyMs: number;
	    ok: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new MirrorResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.template = source["template"];
	        this.latencyMs = source["latencyMs"];
	        this.ok = source["ok"];
	        this.error = source["error"];
	    }
	}

}

export namespace github {
	
	export class ContributionDay {
	    date: string;
	    count: number;
	    level: number;
	
	    static createFrom(source: any = {}) {
	        return new ContributionDay(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.count = source["count"];
	        this.level = source["level"];
	    }
	}
	export class ContributionWeek {
	    days: ContributionDay[];
	
	    static createFrom(source: any = {}) {
	        return new ContributionWeek(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.days = this.convertValues(source["days"], ContributionDay);
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
	export class ContributionCalendar {
	    total: number;
	    weeks: ContributionWeek[];
	
	    static createFrom(source: any = {}) {
	        return new ContributionCalendar(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.weeks = this.convertValues(source["weeks"], ContributionWeek);
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
	
	
	export class DeviceCode {
	    device_code: string;
	    user_code: string;
	    verification_uri: string;
	    expires_in: number;
	    interval: number;
	
	    static createFrom(source: any = {}) {
	        return new DeviceCode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.device_code = source["device_code"];
	        this.user_code = source["user_code"];
	        this.verification_uri = source["verification_uri"];
	        this.expires_in = source["expires_in"];
	        this.interval = source["interval"];
	    }
	}

}

export namespace installer {
	
	export class Result {
	    pending: boolean;
	    needReboot: boolean;
	    unverified: boolean;
	    message: string;
	    exitCode: number;
	    productCode: string;
	    installDir: string;
	    entry: string;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pending = source["pending"];
	        this.needReboot = source["needReboot"];
	        this.unverified = source["unverified"];
	        this.message = source["message"];
	        this.exitCode = source["exitCode"];
	        this.productCode = source["productCode"];
	        this.installDir = source["installDir"];
	        this.entry = source["entry"];
	        this.size = source["size"];
	    }
	}

}

export namespace ledger {
	
	export class InstalledApp {
	    id: string;
	    owner: string;
	    repo: string;
	    displayName: string;
	    installedVersion: string;
	    installedVersionCode: number;
	    installedAssetName: string;
	    installedAssetId: number;
	    installedAssetDigest: string;
	    installedAssetUrl: string;
	    installedAssetSize: number;
	    installedReleaseId: number;
	    installedReleaseTag: string;
	    installedReleasePublishedAt: number;
	    installPath: string;
	    installDir: string;
	    installSize: number;
	    installSource: string;
	    productCode: string;
	    uninstallString: string;
	    libraryStatus: string;
	    uninstalledAt: number;
	    appSlug: string;
	    sourceHost: string;
	    latestVersion: string;
	    latestVersionCode: number;
	    latestAssetName: string;
	    latestAssetId: number;
	    latestAssetDigest: string;
	    latestAssetUrl: string;
	    latestAssetSize: number;
	    latestReleaseId: number;
	    latestReleaseTag: string;
	    latestReleasePublishedAt: number;
	    updateAvailable: boolean;
	    pinned: boolean;
	    updateCheckEnabled: boolean;
	    assetGlob: string;
	    preferredVariant: string;
	    pickedIndex: number;
	    siblingCount: number;
	    includePrereleases: boolean;
	    fallbackToOlderReleases: boolean;
	    assetFilterRegex: string;
	    skippedReleaseTag: string;
	    skippedPublishedAt: number;
	    pendingInstallPath: string;
	    pendingVersion: string;
	    pendingAssetName: string;
	    pendingReleaseTag: string;
	    pendingAssetId: number;
	    pendingDigest: string;
	    installedAt: number;
	    lastCheckedAt: number;
	    lastCheckError: string;
	
	    static createFrom(source: any = {}) {
	        return new InstalledApp(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.owner = source["owner"];
	        this.repo = source["repo"];
	        this.displayName = source["displayName"];
	        this.installedVersion = source["installedVersion"];
	        this.installedVersionCode = source["installedVersionCode"];
	        this.installedAssetName = source["installedAssetName"];
	        this.installedAssetId = source["installedAssetId"];
	        this.installedAssetDigest = source["installedAssetDigest"];
	        this.installedAssetUrl = source["installedAssetUrl"];
	        this.installedAssetSize = source["installedAssetSize"];
	        this.installedReleaseId = source["installedReleaseId"];
	        this.installedReleaseTag = source["installedReleaseTag"];
	        this.installedReleasePublishedAt = source["installedReleasePublishedAt"];
	        this.installPath = source["installPath"];
	        this.installDir = source["installDir"];
	        this.installSize = source["installSize"];
	        this.installSource = source["installSource"];
	        this.productCode = source["productCode"];
	        this.uninstallString = source["uninstallString"];
	        this.libraryStatus = source["libraryStatus"];
	        this.uninstalledAt = source["uninstalledAt"];
	        this.appSlug = source["appSlug"];
	        this.sourceHost = source["sourceHost"];
	        this.latestVersion = source["latestVersion"];
	        this.latestVersionCode = source["latestVersionCode"];
	        this.latestAssetName = source["latestAssetName"];
	        this.latestAssetId = source["latestAssetId"];
	        this.latestAssetDigest = source["latestAssetDigest"];
	        this.latestAssetUrl = source["latestAssetUrl"];
	        this.latestAssetSize = source["latestAssetSize"];
	        this.latestReleaseId = source["latestReleaseId"];
	        this.latestReleaseTag = source["latestReleaseTag"];
	        this.latestReleasePublishedAt = source["latestReleasePublishedAt"];
	        this.updateAvailable = source["updateAvailable"];
	        this.pinned = source["pinned"];
	        this.updateCheckEnabled = source["updateCheckEnabled"];
	        this.assetGlob = source["assetGlob"];
	        this.preferredVariant = source["preferredVariant"];
	        this.pickedIndex = source["pickedIndex"];
	        this.siblingCount = source["siblingCount"];
	        this.includePrereleases = source["includePrereleases"];
	        this.fallbackToOlderReleases = source["fallbackToOlderReleases"];
	        this.assetFilterRegex = source["assetFilterRegex"];
	        this.skippedReleaseTag = source["skippedReleaseTag"];
	        this.skippedPublishedAt = source["skippedPublishedAt"];
	        this.pendingInstallPath = source["pendingInstallPath"];
	        this.pendingVersion = source["pendingVersion"];
	        this.pendingAssetName = source["pendingAssetName"];
	        this.pendingReleaseTag = source["pendingReleaseTag"];
	        this.pendingAssetId = source["pendingAssetId"];
	        this.pendingDigest = source["pendingDigest"];
	        this.installedAt = source["installedAt"];
	        this.lastCheckedAt = source["lastCheckedAt"];
	        this.lastCheckError = source["lastCheckError"];
	    }
	}

}

export namespace main {
	
	export class DownloadRequest {
	    repoKey: string;
	    tag: string;
	    displayName: string;
	    assetId: number;
	    assetName: string;
	    downloadUrl: string;
	    size: number;
	    digest: string;
	    publishedAt: number;
	    confirmUntrusted: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DownloadRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.repoKey = source["repoKey"];
	        this.tag = source["tag"];
	        this.displayName = source["displayName"];
	        this.assetId = source["assetId"];
	        this.assetName = source["assetName"];
	        this.downloadUrl = source["downloadUrl"];
	        this.size = source["size"];
	        this.digest = source["digest"];
	        this.publishedAt = source["publishedAt"];
	        this.confirmUntrusted = source["confirmUntrusted"];
	    }
	}
	export class ExternalApp {
	    name: string;
	    version: string;
	    publisher: string;
	    installDir: string;
	    displayIcon: string;
	    productCode: string;
	    uninstallString: string;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new ExternalApp(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.publisher = source["publisher"];
	        this.installDir = source["installDir"];
	        this.displayIcon = source["displayIcon"];
	        this.productCode = source["productCode"];
	        this.uninstallString = source["uninstallString"];
	        this.size = source["size"];
	    }
	}
	export class ImportPreview {
	    key: string;
	    name: string;
	    status: string;
	    reason?: string;
	
	    static createFrom(source: any = {}) {
	        return new ImportPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.name = source["name"];
	        this.status = source["status"];
	        this.reason = source["reason"];
	    }
	}
	export class ImportReport {
	    total: number;
	    added: number;
	    skipped: number;
	    invalid: number;
	    rows: ImportPreview[];
	
	    static createFrom(source: any = {}) {
	        return new ImportReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.added = source["added"];
	        this.skipped = source["skipped"];
	        this.invalid = source["invalid"];
	        this.rows = this.convertValues(source["rows"], ImportPreview);
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
	export class SearchPage {
	    items: catalog.RepoInfo[];
	    total: number;
	    page: number;
	    pageSize: number;
	    hasMore: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SearchPage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], catalog.RepoInfo);
	        this.total = source["total"];
	        this.page = source["page"];
	        this.pageSize = source["pageSize"];
	        this.hasMore = source["hasMore"];
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

export namespace selfupdate {
	
	export class Info {
	    available: boolean;
	    current: string;
	    latest: string;
	    tag: string;
	    name: string;
	    url: string;
	    size: number;
	    digest: string;
	    notes: string;
	    // Go type: time
	    publishedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Info(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.current = source["current"];
	        this.latest = source["latest"];
	        this.tag = source["tag"];
	        this.name = source["name"];
	        this.url = source["url"];
	        this.size = source["size"];
	        this.digest = source["digest"];
	        this.notes = source["notes"];
	        this.publishedAt = this.convertValues(source["publishedAt"], null);
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

