# external-dns-dnsimple-webhook

Webhook provider for ExternalDNS that manages DNSimple records, including SRV
records with DNSimple's separate `priority` field.

## Why

ExternalDNS has a built-in DNSimple provider, but its SRV support is incomplete
for this use case: it sends the whole SRV target as `content`, while DNSimple
expects `priority` separately and `content` as `weight port target`.

This webhook keeps ExternalDNS as the controller and implements only the provider
side.

## Configuration

Required environment variables:

- `DNSIMPLE_OAUTH`: DNSimple API token.
- `DNSIMPLE_ZONE`: DNSimple zone, for example `example.com`.
- `DOMAIN_FILTER`: comma-separated domain filters served by this webhook, for
  example `apps.example.com`.

Optional:

- `DNSIMPLE_ACCOUNT_ID`: DNSimple account ID. If omitted, `/whoami` is used.
- `DNSIMPLE_BASE_URL`: defaults to `https://api.dnsimple.com/v2`.
- `WEBHOOK_ADDR`: defaults to `127.0.0.1:8888`.
- `HEALTH_ADDR`: defaults to `:8080`.

## ExternalDNS Helm Values

```yaml
provider:
  name: webhook
  webhook:
    image:
      repository: ghcr.io/example/external-dns-dnsimple-webhook
      tag: latest
    env:
      - name: DNSIMPLE_OAUTH
        valueFrom:
          secretKeyRef:
            name: external-dns-dnsimple
            key: token
      - name: DNSIMPLE_ACCOUNT_ID
        value: "12345"
      - name: DNSIMPLE_ZONE
        value: example.com
      - name: DOMAIN_FILTER
        value: apps.example.com

sources:
  - crd
domainFilters:
  - apps.example.com
managedRecordTypes:
  - A
  - CNAME
  - SRV
registry: txt
txtOwnerId: example
txtPrefix: _external-dns-
policy: sync
triggerLoopOnEvent: true
```

## SRV Mapping

ExternalDNS endpoint target:

```text
0 50 47027 db-0.mongodb.apps.example.com
```

DNSimple API payload:

```json
{
  "type": "SRV",
  "priority": 0,
  "content": "50 47027 db-0.mongodb.apps.example.com"
}
```
