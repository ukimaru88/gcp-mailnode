export namespace deploy {
	
	export class BatchProgress {
	    id: string;
	    total: number;
	    succeeded: number;
	    failed: number;
	    status: string;
	
	    static createFrom(source: any = {}) {
	        return new BatchProgress(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.total = source["total"];
	        this.succeeded = source["succeeded"];
	        this.failed = source["failed"];
	        this.status = source["status"];
	    }
	}
	export class BatchRequest {
	    gcp_cred_ids: string[];
	    aliyun_cred_id: string;
	    template_id: string;
	    count: number;
	    regions: string[];
	    domains: string[];
	    root_password: string;
	    dnsbl_threshold: number;
	    max_retry_per_slot: number;
	
	    static createFrom(source: any = {}) {
	        return new BatchRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.gcp_cred_ids = source["gcp_cred_ids"];
	        this.aliyun_cred_id = source["aliyun_cred_id"];
	        this.template_id = source["template_id"];
	        this.count = source["count"];
	        this.regions = source["regions"];
	        this.domains = source["domains"];
	        this.root_password = source["root_password"];
	        this.dnsbl_threshold = source["dnsbl_threshold"];
	        this.max_retry_per_slot = source["max_retry_per_slot"];
	    }
	}
	export class DeployOpts {
	    hide_client_ip: boolean;
	    persona_id: string;
	    deploy_type: string;
	    mail_user: string;
	
	    static createFrom(source: any = {}) {
	        return new DeployOpts(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hide_client_ip = source["hide_client_ip"];
	        this.persona_id = source["persona_id"];
	        this.deploy_type = source["deploy_type"];
	        this.mail_user = source["mail_user"];
	    }
	}
	export class StageARequest {
	    gcp_cred_ids: string[];
	    template_id: string;
	    count: number;
	    regions: string[];
	    dnsbl_threshold: number;
	    max_retry_per_slot: number;
	    ip_prefix_filter: string[];
	    ip_prefix_exclude: string[];
	    nic_count: number;
	    skip_dnsbl: boolean;
	
	    static createFrom(source: any = {}) {
	        return new StageARequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.gcp_cred_ids = source["gcp_cred_ids"];
	        this.template_id = source["template_id"];
	        this.count = source["count"];
	        this.regions = source["regions"];
	        this.dnsbl_threshold = source["dnsbl_threshold"];
	        this.max_retry_per_slot = source["max_retry_per_slot"];
	        this.ip_prefix_filter = source["ip_prefix_filter"];
	        this.ip_prefix_exclude = source["ip_prefix_exclude"];
	        this.nic_count = source["nic_count"];
	        this.skip_dnsbl = source["skip_dnsbl"];
	    }
	}
	export class StageBRequest {
	    template_id: string;
	    root_password: string;
	
	    static createFrom(source: any = {}) {
	        return new StageBRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.template_id = source["template_id"];
	        this.root_password = source["root_password"];
	    }
	}
	export class StageCRequest {
	    domain_ip_map: Record<string, string>;
	    aliyun_cred_id: string;
	    hide_client_ip: boolean;
	    persona_id: string;
	    root_domain_map: Record<string, string>;
	    deploy_type: string;
	    mail_user: string;
	
	    static createFrom(source: any = {}) {
	        return new StageCRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.domain_ip_map = source["domain_ip_map"];
	        this.aliyun_cred_id = source["aliyun_cred_id"];
	        this.hide_client_ip = source["hide_client_ip"];
	        this.persona_id = source["persona_id"];
	        this.root_domain_map = source["root_domain_map"];
	        this.deploy_type = source["deploy_type"];
	        this.mail_user = source["mail_user"];
	    }
	}

}

export namespace main {
	
	export class AliyunCredentialDTO {
	    id: string;
	    name: string;
	    access_key_id: string;
	    enabled: boolean;
	    // Go type: time
	    created_at: any;
	
	    static createFrom(source: any = {}) {
	        return new AliyunCredentialDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.access_key_id = source["access_key_id"];
	        this.enabled = source["enabled"];
	        this.created_at = this.convertValues(source["created_at"], null);
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
	export class BatchTaskDTO {
	    id: string;
	    status: string;
	    total: number;
	    succeeded: number;
	    failed: number;
	    // Go type: time
	    started_at: any;
	    // Go type: time
	    finished_at?: any;
	
	    static createFrom(source: any = {}) {
	        return new BatchTaskDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.status = source["status"];
	        this.total = source["total"];
	        this.succeeded = source["succeeded"];
	        this.failed = source["failed"];
	        this.started_at = this.convertValues(source["started_at"], null);
	        this.finished_at = this.convertValues(source["finished_at"], null);
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
	export class BlackSegmentDTO {
	    id: number;
	    cidr: string;
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new BlackSegmentDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.cidr = source["cidr"];
	        this.note = source["note"];
	    }
	}
	export class DNSRecordDTO {
	    id: string;
	    aliyun_cred_id: string;
	    domain: string;
	    rr: string;
	    record_type: string;
	    value: string;
	    aliyun_record_id: string;
	    related_instance_id: string;
	    // Go type: time
	    created_at: any;
	
	    static createFrom(source: any = {}) {
	        return new DNSRecordDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.aliyun_cred_id = source["aliyun_cred_id"];
	        this.domain = source["domain"];
	        this.rr = source["rr"];
	        this.record_type = source["record_type"];
	        this.value = source["value"];
	        this.aliyun_record_id = source["aliyun_record_id"];
	        this.related_instance_id = source["related_instance_id"];
	        this.created_at = this.convertValues(source["created_at"], null);
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
	export class ExtractResult {
	    vps_id: string;
	    name: string;
	    ip: string;
	    lines: number;
	    parsed: number;
	    emails: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ExtractResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vps_id = source["vps_id"];
	        this.name = source["name"];
	        this.ip = source["ip"];
	        this.lines = source["lines"];
	        this.parsed = source["parsed"];
	        this.emails = source["emails"];
	        this.error = source["error"];
	    }
	}
	export class ExtractScheduleConfig {
	    enabled: boolean;
	    interval_min: number;
	    delete_after: boolean;
	    last_run_at: string;
	    last_run_status: string;
	    last_run_msg: string;
	    next_run_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ExtractScheduleConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.interval_min = source["interval_min"];
	        this.delete_after = source["delete_after"];
	        this.last_run_at = source["last_run_at"];
	        this.last_run_status = source["last_run_status"];
	        this.last_run_msg = source["last_run_msg"];
	        this.next_run_at = source["next_run_at"];
	    }
	}
	export class WriteSummary {
	    total_emails: number;
	    new_emails: number;
	    duplicate_skip: number;
	    files_created: string[];
	
	    static createFrom(source: any = {}) {
	        return new WriteSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total_emails = source["total_emails"];
	        this.new_emails = source["new_emails"];
	        this.duplicate_skip = source["duplicate_skip"];
	        this.files_created = source["files_created"];
	    }
	}
	export class ExtractSummary {
	    batch_id: string;
	    results: ExtractResult[];
	    output_dir: string;
	    total_emails: number;
	    write_result: WriteSummary;
	
	    static createFrom(source: any = {}) {
	        return new ExtractSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.batch_id = source["batch_id"];
	        this.results = this.convertValues(source["results"], ExtractResult);
	        this.output_dir = source["output_dir"];
	        this.total_emails = source["total_emails"];
	        this.write_result = this.convertValues(source["write_result"], WriteSummary);
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
	export class GCPCredentialDTO {
	    id: string;
	    name: string;
	    auth_type: string;
	    project_id: string;
	    enabled: boolean;
	    // Go type: time
	    created_at: any;
	
	    static createFrom(source: any = {}) {
	        return new GCPCredentialDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.auth_type = source["auth_type"];
	        this.project_id = source["project_id"];
	        this.enabled = source["enabled"];
	        this.created_at = this.convertValues(source["created_at"], null);
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
	export class GCPMonitorHourlyDTO {
	    end_time: string;
	    sent_gb: number;
	    received_gb: number;
	    total_gb: number;
	    traffic_cost: number;
	
	    static createFrom(source: any = {}) {
	        return new GCPMonitorHourlyDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.end_time = source["end_time"];
	        this.sent_gb = source["sent_gb"];
	        this.received_gb = source["received_gb"];
	        this.total_gb = source["total_gb"];
	        this.traffic_cost = source["traffic_cost"];
	    }
	}
	export class GCPMonitorInstanceDTO {
	    id: string;
	    gcp_instance_id: string;
	    name: string;
	    zone: string;
	    machine_type: string;
	    status: string;
	    ip: string;
	    fqdn: string;
	    sent_gb: number;
	    received_gb: number;
	    total_gb: number;
	    last_hour_sent_gb: number;
	    last_hour_received_gb: number;
	    traffic_cost_24h: number;
	    projected_cost_24h: number;
	
	    static createFrom(source: any = {}) {
	        return new GCPMonitorInstanceDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.gcp_instance_id = source["gcp_instance_id"];
	        this.name = source["name"];
	        this.zone = source["zone"];
	        this.machine_type = source["machine_type"];
	        this.status = source["status"];
	        this.ip = source["ip"];
	        this.fqdn = source["fqdn"];
	        this.sent_gb = source["sent_gb"];
	        this.received_gb = source["received_gb"];
	        this.total_gb = source["total_gb"];
	        this.last_hour_sent_gb = source["last_hour_sent_gb"];
	        this.last_hour_received_gb = source["last_hour_received_gb"];
	        this.traffic_cost_24h = source["traffic_cost_24h"];
	        this.projected_cost_24h = source["projected_cost_24h"];
	    }
	}
	export class GCPMonitorPricing {
	    currency: string;
	    egress_per_gb: number;
	    vps_per_hour: number;
	    static_ip_per_hour: number;
	    use_last_hour_projection: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GCPMonitorPricing(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.currency = source["currency"];
	        this.egress_per_gb = source["egress_per_gb"];
	        this.vps_per_hour = source["vps_per_hour"];
	        this.static_ip_per_hour = source["static_ip_per_hour"];
	        this.use_last_hour_projection = source["use_last_hour_projection"];
	    }
	}
	export class GCPMonitorReport {
	    cred_id: string;
	    cred_name: string;
	    project_id: string;
	    generated_at: string;
	    hours: number;
	    pricing: GCPMonitorPricing;
	    total_vps: number;
	    running_vps: number;
	    total_static_ips: number;
	    in_use_static_ips: number;
	    reserved_static_ips: number;
	    sent_gb: number;
	    received_gb: number;
	    total_gb: number;
	    last_hour_sent_gb: number;
	    last_hour_received_gb: number;
	    projected_sent_gb_24h: number;
	    vps_cost_24h: number;
	    static_ip_cost_24h: number;
	    traffic_cost_24h: number;
	    projected_traffic_cost_24h: number;
	    estimated_cost_24h: number;
	    projected_cost_24h: number;
	    metric_error: string;
	    warnings: string[];
	    instances: GCPMonitorInstanceDTO[];
	    hourly: GCPMonitorHourlyDTO[];
	
	    static createFrom(source: any = {}) {
	        return new GCPMonitorReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cred_id = source["cred_id"];
	        this.cred_name = source["cred_name"];
	        this.project_id = source["project_id"];
	        this.generated_at = source["generated_at"];
	        this.hours = source["hours"];
	        this.pricing = this.convertValues(source["pricing"], GCPMonitorPricing);
	        this.total_vps = source["total_vps"];
	        this.running_vps = source["running_vps"];
	        this.total_static_ips = source["total_static_ips"];
	        this.in_use_static_ips = source["in_use_static_ips"];
	        this.reserved_static_ips = source["reserved_static_ips"];
	        this.sent_gb = source["sent_gb"];
	        this.received_gb = source["received_gb"];
	        this.total_gb = source["total_gb"];
	        this.last_hour_sent_gb = source["last_hour_sent_gb"];
	        this.last_hour_received_gb = source["last_hour_received_gb"];
	        this.projected_sent_gb_24h = source["projected_sent_gb_24h"];
	        this.vps_cost_24h = source["vps_cost_24h"];
	        this.static_ip_cost_24h = source["static_ip_cost_24h"];
	        this.traffic_cost_24h = source["traffic_cost_24h"];
	        this.projected_traffic_cost_24h = source["projected_traffic_cost_24h"];
	        this.estimated_cost_24h = source["estimated_cost_24h"];
	        this.projected_cost_24h = source["projected_cost_24h"];
	        this.metric_error = source["metric_error"];
	        this.warnings = source["warnings"];
	        this.instances = this.convertValues(source["instances"], GCPMonitorInstanceDTO);
	        this.hourly = this.convertValues(source["hourly"], GCPMonitorHourlyDTO);
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
	export class KumoMTADiagnosticDTO {
	    vps_id: string;
	    ip: string;
	    internal_ip: string;
	    fqdn: string;
	    ok: boolean;
	    summary: string;
	    detail: string;
	
	    static createFrom(source: any = {}) {
	        return new KumoMTADiagnosticDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vps_id = source["vps_id"];
	        this.ip = source["ip"];
	        this.internal_ip = source["internal_ip"];
	        this.fqdn = source["fqdn"];
	        this.ok = source["ok"];
	        this.summary = source["summary"];
	        this.detail = source["detail"];
	    }
	}
	export class OrphanCleanupReport {
	    vps_deleted: number;
	    static_ips_deleted: number;
	    dns_records_deleted: number;
	
	    static createFrom(source: any = {}) {
	        return new OrphanCleanupReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vps_deleted = source["vps_deleted"];
	        this.static_ips_deleted = source["static_ips_deleted"];
	        this.dns_records_deleted = source["dns_records_deleted"];
	    }
	}
	export class PersonaExtraHeader {
	    name: string;
	    value: string;
	
	    static createFrom(source: any = {}) {
	        return new PersonaExtraHeader(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.value = source["value"];
	    }
	}
	export class PersonaDTO {
	    id: string;
	    name: string;
	    description: string;
	    received_template: string;
	    user_agent: string;
	    x_mailer: string;
	    extra_headers: PersonaExtraHeader[];
	    is_preset: boolean;
	    // Go type: time
	    created_at: any;
	
	    static createFrom(source: any = {}) {
	        return new PersonaDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.received_template = source["received_template"];
	        this.user_agent = source["user_agent"];
	        this.x_mailer = source["x_mailer"];
	        this.extra_headers = this.convertValues(source["extra_headers"], PersonaExtraHeader);
	        this.is_preset = source["is_preset"];
	        this.created_at = this.convertValues(source["created_at"], null);
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
	
	export class ServerCounterDTO {
	    name: string;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new ServerCounterDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.count = source["count"];
	    }
	}
	export class ServerReasonDTO {
	    reason: string;
	    count: number;
	    top_domains: ServerCounterDTO[];
	    sample: string;
	
	    static createFrom(source: any = {}) {
	        return new ServerReasonDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.reason = source["reason"];
	        this.count = source["count"];
	        this.top_domains = this.convertValues(source["top_domains"], ServerCounterDTO);
	        this.sample = source["sample"];
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
	export class ServerStatusDTO {
	    vps_id: string;
	    name: string;
	    ip: string;
	    fqdn: string;
	    zone: string;
	    checked_at: string;
	    service_active: boolean;
	    service_state: string;
	    service_enabled: string;
	    uptime: string;
	    ports: string[];
	    load_average: string;
	    root_disk_used: string;
	    spool_disk_used: string;
	    queue_files: number;
	    queue_bytes: number;
	    queue_bytes_human: string;
	    meta_files: number;
	    data_files: number;
	    log_files_scanned: number;
	    last_log_file: string;
	    submitted: number;
	    delivered: number;
	    bounced: number;
	    deferred: number;
	    unique_domains: number;
	    top_domains: ServerCounterDTO[];
	    bounce_reasons: ServerReasonDTO[];
	    recent_errors: string[];
	    recommendations: string[];
	    raw_status: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new ServerStatusDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vps_id = source["vps_id"];
	        this.name = source["name"];
	        this.ip = source["ip"];
	        this.fqdn = source["fqdn"];
	        this.zone = source["zone"];
	        this.checked_at = source["checked_at"];
	        this.service_active = source["service_active"];
	        this.service_state = source["service_state"];
	        this.service_enabled = source["service_enabled"];
	        this.uptime = source["uptime"];
	        this.ports = source["ports"];
	        this.load_average = source["load_average"];
	        this.root_disk_used = source["root_disk_used"];
	        this.spool_disk_used = source["spool_disk_used"];
	        this.queue_files = source["queue_files"];
	        this.queue_bytes = source["queue_bytes"];
	        this.queue_bytes_human = source["queue_bytes_human"];
	        this.meta_files = source["meta_files"];
	        this.data_files = source["data_files"];
	        this.log_files_scanned = source["log_files_scanned"];
	        this.last_log_file = source["last_log_file"];
	        this.submitted = source["submitted"];
	        this.delivered = source["delivered"];
	        this.bounced = source["bounced"];
	        this.deferred = source["deferred"];
	        this.unique_domains = source["unique_domains"];
	        this.top_domains = this.convertValues(source["top_domains"], ServerCounterDTO);
	        this.bounce_reasons = this.convertValues(source["bounce_reasons"], ServerReasonDTO);
	        this.recent_errors = source["recent_errors"];
	        this.recommendations = source["recommendations"];
	        this.raw_status = source["raw_status"];
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
	export class StaticIPDTO {
	    id: string;
	    gcp_cred_id: string;
	    gcp_address_name: string;
	    ip: string;
	    region: string;
	    status: string;
	    bound_instance_id: string;
	    dnsbl_result: string;
	    dnsbl_hit_lists: string;
	    batch_id: string;
	    slot_group: string;
	    nic_index: number;
	    // Go type: time
	    created_at: any;
	
	    static createFrom(source: any = {}) {
	        return new StaticIPDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.gcp_cred_id = source["gcp_cred_id"];
	        this.gcp_address_name = source["gcp_address_name"];
	        this.ip = source["ip"];
	        this.region = source["region"];
	        this.status = source["status"];
	        this.bound_instance_id = source["bound_instance_id"];
	        this.dnsbl_result = source["dnsbl_result"];
	        this.dnsbl_hit_lists = source["dnsbl_hit_lists"];
	        this.batch_id = source["batch_id"];
	        this.slot_group = source["slot_group"];
	        this.nic_index = source["nic_index"];
	        this.created_at = this.convertValues(source["created_at"], null);
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
	export class VPSInstanceDTO {
	    id: string;
	    gcp_cred_id: string;
	    gcp_instance_id: string;
	    name: string;
	    region: string;
	    zone: string;
	    machine_type: string;
	    status: string;
	    ip: string;
	    internal_ip: string;
	    fqdn: string;
	    root_password: string;
	    deploy_status: string;
	    deploy_error: string;
	    ptr_status: string;
	    smtp_account: string;
	    smtp_password: string;
	    dkim_public_key: string;
	    aliyun_cred_id: string;
	    domain: string;
	    batch_id: string;
	    deploy_type: string;
	    // Go type: time
	    created_at: any;
	
	    static createFrom(source: any = {}) {
	        return new VPSInstanceDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.gcp_cred_id = source["gcp_cred_id"];
	        this.gcp_instance_id = source["gcp_instance_id"];
	        this.name = source["name"];
	        this.region = source["region"];
	        this.zone = source["zone"];
	        this.machine_type = source["machine_type"];
	        this.status = source["status"];
	        this.ip = source["ip"];
	        this.internal_ip = source["internal_ip"];
	        this.fqdn = source["fqdn"];
	        this.root_password = source["root_password"];
	        this.deploy_status = source["deploy_status"];
	        this.deploy_error = source["deploy_error"];
	        this.ptr_status = source["ptr_status"];
	        this.smtp_account = source["smtp_account"];
	        this.smtp_password = source["smtp_password"];
	        this.dkim_public_key = source["dkim_public_key"];
	        this.aliyun_cred_id = source["aliyun_cred_id"];
	        this.domain = source["domain"];
	        this.batch_id = source["batch_id"];
	        this.deploy_type = source["deploy_type"];
	        this.created_at = this.convertValues(source["created_at"], null);
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
	export class VPSTemplateDTO {
	    id: string;
	    name: string;
	    regions: string[];
	    auto_spread: boolean;
	    machine_type: string;
	    image_family: string;
	    image_project: string;
	    disk_size_gb: number;
	    disk_type: string;
	    tags: string[];
	    metadata_script: string;
	    root_password: string;
	    deploy_type: string;
	    provisioning_model: string;
	    nic_count: number;
	    is_preset: boolean;
	    // Go type: time
	    created_at: any;
	
	    static createFrom(source: any = {}) {
	        return new VPSTemplateDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.regions = source["regions"];
	        this.auto_spread = source["auto_spread"];
	        this.machine_type = source["machine_type"];
	        this.image_family = source["image_family"];
	        this.image_project = source["image_project"];
	        this.disk_size_gb = source["disk_size_gb"];
	        this.disk_type = source["disk_type"];
	        this.tags = source["tags"];
	        this.metadata_script = source["metadata_script"];
	        this.root_password = source["root_password"];
	        this.deploy_type = source["deploy_type"];
	        this.provisioning_model = source["provisioning_model"];
	        this.nic_count = source["nic_count"];
	        this.is_preset = source["is_preset"];
	        this.created_at = this.convertValues(source["created_at"], null);
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

