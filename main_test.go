package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSRVTarget(t *testing.T) {
	target, err := parseSRVTarget("0 50 47027 db-0.mongodb.apps.example.com")
	if err != nil {
		t.Fatalf("parseSRVTarget returned error: %v", err)
	}

	if target.Priority != 0 || target.Weight != 50 || target.Port != 47027 {
		t.Fatalf("unexpected target: %#v", target)
	}
	if target.Host != "db-0.mongodb.apps.example.com" {
		t.Fatalf("unexpected host: %s", target.Host)
	}
}

func TestEndpointToPayloadSRV(t *testing.T) {
	ep := endpoint{
		DNSName:    "_mongodb._tcp.mongodb.apps.example.com",
		RecordType: "SRV",
		RecordTTL:  60,
		Targets: []string{
			"0 50 47027 db-0.mongodb.apps.example.com",
		},
	}

	payload, err := endpointToPayload(ep, ep.Targets[0], "example.com", "_external-dns-")
	if err != nil {
		t.Fatalf("endpointToPayload returned error: %v", err)
	}

	if payload.Name != "_mongodb._tcp.mongodb.apps" {
		t.Fatalf("unexpected name: %s", payload.Name)
	}
	if payload.Type != "SRV" {
		t.Fatalf("unexpected type: %s", payload.Type)
	}
	if payload.Priority == nil || *payload.Priority != 0 {
		t.Fatalf("unexpected priority: %#v", payload.Priority)
	}
	if payload.Content != "50 47027 db-0.mongodb.apps.example.com" {
		t.Fatalf("unexpected content: %s", payload.Content)
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json marshal failed: %v", err)
	}
	if !strings.Contains(string(data), `"priority":0`) {
		t.Fatalf("payload does not include priority: %s", string(data))
	}
}

func TestRecordToEndpointSRV(t *testing.T) {
	priority := 0
	ep, ok := recordToEndpoint(dnsimpleRecord{
		Name:     "_mongodb._tcp.mongodb.apps",
		Type:     "SRV",
		Content:  "50 47027 db-0.mongodb.apps.example.com",
		TTL:      60,
		Priority: &priority,
	}, "example.com", "_external-dns-")
	if !ok {
		t.Fatal("record was not converted")
	}

	if ep.DNSName != "_mongodb._tcp.mongodb.apps.example.com" {
		t.Fatalf("unexpected dns name: %s", ep.DNSName)
	}
	if len(ep.Targets) != 1 || ep.Targets[0] != "0 50 47027 db-0.mongodb.apps.example.com" {
		t.Fatalf("unexpected targets: %#v", ep.Targets)
	}
}

func TestRecordsToEndpointsGroupsTargets(t *testing.T) {
	priority := 0
	endpoints := recordsToEndpoints([]dnsimpleRecord{
		{
			Name:     "_mongodb._tcp.mongodb.apps",
			Type:     "SRV",
			Content:  "50 47027 db-0.mongodb.apps.example.com",
			TTL:      60,
			Priority: &priority,
		},
		{
			Name:     "_mongodb._tcp.mongodb.apps",
			Type:     "SRV",
			Content:  "50 47027 db-1.mongodb.apps.example.com",
			TTL:      60,
			Priority: &priority,
		},
	}, "example.com", "_external-dns-")

	if len(endpoints) != 1 {
		t.Fatalf("expected one grouped endpoint, got %#v", endpoints)
	}
	if len(endpoints[0].Targets) != 2 {
		t.Fatalf("unexpected grouped targets: %#v", endpoints[0].Targets)
	}
}

func TestNormalizeSRVTXTName(t *testing.T) {
	name := "_external-dns-srv-_mongodb._tcp.mongodb.apps"
	normalized := normalizeSRVTXTName(name, "_external-dns-")
	if normalized != "_external-dns-_mongodb._tcp.mongodb.apps" {
		t.Fatalf("unexpected normalized name: %s", normalized)
	}
}

func TestEndpointToPayloadNormalizesSRVTXTName(t *testing.T) {
	ep := endpoint{
		DNSName:    "_external-dns-srv-_mongodb._tcp.mongodb.apps.example.com",
		RecordType: "TXT",
		Targets: []string{
			"heritage=external-dns,external-dns/owner=development,external-dns/resource=crd/ns/name",
		},
	}

	payload, err := endpointToPayload(ep, ep.Targets[0], "example.com", "_external-dns-")
	if err != nil {
		t.Fatalf("endpointToPayload returned error: %v", err)
	}
	if payload.Name != "_external-dns-_mongodb._tcp.mongodb.apps" {
		t.Fatalf("unexpected payload name: %s", payload.Name)
	}
}

func TestRelativeNameRejectsOutsideZone(t *testing.T) {
	_, err := relativeName("other.test", "example.com")
	if err == nil {
		t.Fatal("expected error")
	}
}
