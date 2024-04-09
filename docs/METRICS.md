# Metrics

| Name| Type | Labels |
| ---------- | ----------- | ----------- |
| spegel_advertised_images | Gauge | `registry` |
| spegel_resolve_duration_seconds | Histogram | `router` |
| spegel_advertised_keys | Gauge | `registry` |
| spegel_advertised_image_tags | Gauge | `registry` |
| spegel_advertised_image_digests | Gauge | `registry` |
| spegel_mirror_requests_total | Counter | `registry` <br/> `cache=hit\|miss` <br/> `source=internal\|external` |
| http_request_duration_seconds | Histogram | `handler` <br/> `method` <br/> `code` |
| http_response_size_bytes | Histogram | `handler` <br/> `method` <br/> `code` |
| http_requests_inflight | Gauge | `handler` |
