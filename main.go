package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const mediaType = "application/external.dns.webhook+json;version=1"
const txtSetIdentifierLabel = "external-dns/set-identifier"

type endpoint struct {
	DNSName          string                     `json:"dnsName"`
	Targets          []string                   `json:"targets,omitempty"`
	RecordType       string                     `json:"recordType"`
	SetIdentifier    string                     `json:"setIdentifier,omitempty"`
	RecordTTL        int                        `json:"recordTTL,omitempty"`
	Labels           map[string]string          `json:"labels,omitempty"`
	ProviderSpecific []providerSpecificProperty `json:"providerSpecific,omitempty"`
}

type providerSpecificProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type changes struct {
	Create    []endpoint `json:"create,omitempty"`
	UpdateOld []endpoint `json:"updateOld,omitempty"`
	UpdateNew []endpoint `json:"updateNew,omitempty"`
	Delete    []endpoint `json:"delete,omitempty"`
}

type filtersResponse struct {
	Filters []string `json:"filters"`
}

type config struct {
	BaseURL       string
	AccountID     string
	Zone          string
	DomainFilters []string
	Token         string
	TXTPrefix     string
	WebhookAddr   string
	HealthAddr    string
}

type dnsimpleClient struct {
	httpClient *http.Client
	baseURL    string
	accountID  string
	token      string
	zone       string
	txtPrefix  string
}

type dnsimpleRecord struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority *int   `json:"priority,omitempty"`
}

type listRecordsResponse struct {
	Data       []dnsimpleRecord `json:"data"`
	Pagination pagination       `json:"pagination"`
}

type pagination struct {
	CurrentPage int `json:"current_page"`
	TotalPages  int `json:"total_pages"`
}

type whoamiResponse struct {
	Data struct {
		Account struct {
			ID int64 `json:"id"`
		} `json:"account"`
	} `json:"data"`
}

type recordPayload struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      *int   `json:"ttl,omitempty"`
	Priority *int   `json:"priority,omitempty"`
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	client := &dnsimpleClient{
		httpClient: &http.Client{Timeout: 20 * time.Second},
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		accountID:  cfg.AccountID,
		token:      cfg.Token,
		zone:       cfg.Zone,
		txtPrefix:  cfg.TXTPrefix,
	}

	if client.accountID == "" {
		accountID, err := client.whoami(context.Background())
		if err != nil {
			log.Fatalf("failed to discover DNSimple account id: %v", err)
		}
		client.accountID = accountID
	}

	app := &server{
		cfg:    cfg,
		client: client,
	}

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/", app.handleNegotiate)
	apiMux.HandleFunc("/records", app.handleRecords)
	apiMux.HandleFunc("/adjustendpoints", app.handleAdjustEndpoints)
	apiMux.HandleFunc("/healthz", handleHealthz)

	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", handleHealthz)
	healthMux.HandleFunc("/metrics", handleMetrics)

	errCh := make(chan error, 2)
	go func() {
		log.Printf("starting webhook API on %s for zone %s", cfg.WebhookAddr, cfg.Zone)
		errCh <- http.ListenAndServe(cfg.WebhookAddr, apiMux)
	}()
	go func() {
		log.Printf("starting health server on %s", cfg.HealthAddr)
		errCh <- http.ListenAndServe(cfg.HealthAddr, healthMux)
	}()

	log.Fatal(<-errCh)
}

type server struct {
	cfg    config
	client *dnsimpleClient
}

func loadConfig() (config, error) {
	cfg := config{
		BaseURL:     getenv("DNSIMPLE_BASE_URL", "https://api.dnsimple.com/v2"),
		AccountID:   os.Getenv("DNSIMPLE_ACCOUNT_ID"),
		Zone:        os.Getenv("DNSIMPLE_ZONE"),
		Token:       os.Getenv("DNSIMPLE_OAUTH"),
		TXTPrefix:   getenv("TXT_PREFIX", "_external-dns-"),
		WebhookAddr: getenv("WEBHOOK_ADDR", "127.0.0.1:8888"),
		HealthAddr:  getenv("HEALTH_ADDR", ":8080"),
	}

	if cfg.Token == "" {
		return cfg, errors.New("DNSIMPLE_OAUTH is required")
	}
	if cfg.Zone == "" {
		return cfg, errors.New("DNSIMPLE_ZONE is required")
	}

	rawFilters := getenv("DOMAIN_FILTER", cfg.Zone)
	for _, item := range strings.Split(rawFilters, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			cfg.DomainFilters = append(cfg.DomainFilters, item)
		}
	}
	if len(cfg.DomainFilters) == 0 {
		return cfg, errors.New("DOMAIN_FILTER must include at least one domain")
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func (s *server) handleNegotiate(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, filtersResponse{Filters: s.cfg.DomainFilters})
}

func (s *server) handleRecords(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		records, err := s.client.listEndpoints(r.Context())
		if err != nil {
			httpError(w, "failed to list records", err)
			return
		}
		writeJSON(w, http.StatusOK, records)
	case http.MethodPost:
		var changeSet changes
		if err := decodeJSON(r, &changeSet); err != nil {
			httpError(w, "failed to decode changes", err)
			return
		}
		if err := s.client.applyChanges(r.Context(), changeSet); err != nil {
			httpError(w, "failed to apply changes", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *server) handleAdjustEndpoints(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var endpoints []endpoint
	if err := decodeJSON(r, &endpoints); err != nil {
		httpError(w, "failed to decode endpoints", err)
		return
	}
	writeJSON(w, http.StatusOK, endpoints)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("# no metrics exported\n"))
}

func decodeJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(value)
	}
}

func httpError(w http.ResponseWriter, msg string, err error) {
	log.Printf("%s: %v", msg, err)
	http.Error(w, msg+": "+err.Error(), http.StatusInternalServerError)
}

func (c *dnsimpleClient) listEndpoints(ctx context.Context) ([]endpoint, error) {
	records, err := c.listRecords(ctx, "")
	if err != nil {
		return nil, err
	}

	return recordsToEndpoints(records, c.zone, c.txtPrefix), nil
}

func recordsToEndpoints(records []dnsimpleRecord, zone, txtPrefix string) []endpoint {
	endpoints := make([]endpoint, 0, len(records))
	endpointIndexes := make(map[string]int, len(records))
	for _, record := range records {
		ep, ok := recordToEndpoint(record, zone, txtPrefix)
		if ok {
			key := endpointGroupKey(ep)
			if index, exists := endpointIndexes[key]; exists {
				endpoints[index].Targets = append(endpoints[index].Targets, ep.Targets...)
				continue
			}
			endpointIndexes[key] = len(endpoints)
			endpoints = append(endpoints, ep)
		}
	}
	return endpoints
}

func endpointGroupKey(ep endpoint) string {
	return strings.Join([]string{
		ep.DNSName,
		ep.RecordType,
		ep.SetIdentifier,
		strconv.Itoa(ep.RecordTTL),
	}, "\x00")
}

func recordToEndpoint(record dnsimpleRecord, zone, txtPrefix string) (endpoint, bool) {
	dnsName := record.Name
	if record.Type == "TXT" {
		dnsName = normalizeSRVTXTName(dnsName, txtPrefix)
	}
	if dnsName == "" {
		dnsName = zone
	} else {
		dnsName = dnsName + "." + zone
	}

	target := record.Content
	if record.Type == "SRV" {
		priority := 0
		if record.Priority != nil {
			priority = *record.Priority
		}
		target = fmt.Sprintf("%d %s", priority, record.Content)
	}

	switch record.Type {
	case "A", "AAAA", "CNAME", "TXT", "SRV":
		ep := endpoint{
			DNSName:    dnsName,
			RecordType: record.Type,
			RecordTTL:  record.TTL,
			Targets:    []string{target},
		}
		if record.Type == "SRV" {
			ep.SetIdentifier = srvSetIdentifier(target)
		}
		if record.Type == "TXT" {
			ep.SetIdentifier = txtSetIdentifier(target)
		}
		return ep, true
	default:
		return endpoint{}, false
	}
}

func (c *dnsimpleClient) applyChanges(ctx context.Context, changeSet changes) error {
	for _, ep := range changeSet.Delete {
		if err := c.deleteEndpoint(ctx, ep); err != nil {
			return err
		}
	}

	for i, oldEP := range changeSet.UpdateOld {
		if err := c.deleteEndpoint(ctx, oldEP); err != nil {
			return err
		}
		if i < len(changeSet.UpdateNew) {
			if err := c.createEndpoint(ctx, changeSet.UpdateNew[i]); err != nil {
				return err
			}
		}
	}

	for _, ep := range changeSet.Create {
		if err := c.createEndpoint(ctx, ep); err != nil {
			return err
		}
	}

	return nil
}

func (c *dnsimpleClient) createEndpoint(ctx context.Context, ep endpoint) error {
	if len(ep.Targets) == 0 && ep.RecordType != "A" && ep.RecordType != "AAAA" && ep.RecordType != "CNAME" {
		return nil
	}

	for _, target := range ep.Targets {
		payload, err := endpointToPayload(ep, target, c.zone, c.txtPrefix)
		if err != nil {
			return err
		}
		if _, ok, err := c.findRecord(ctx, payload); err != nil {
			return err
		} else if ok {
			log.Printf("record already exists: %s %s -> %s", payload.Type, ep.DNSName, payload.Content)
			continue
		}
		log.Printf("creating %s %s -> %s", payload.Type, ep.DNSName, payload.Content)
		if err := c.createRecord(ctx, payload); err != nil {
			return err
		}
	}
	return nil
}

func (c *dnsimpleClient) deleteEndpoint(ctx context.Context, ep endpoint) error {
	for _, target := range ep.Targets {
		payload, err := endpointToPayload(ep, target, c.zone, c.txtPrefix)
		if err != nil {
			return err
		}
		record, ok, err := c.findRecord(ctx, payload)
		if err != nil {
			return err
		}
		if !ok && ep.RecordType == "TXT" {
			rawPayload, rawErr := endpointToPayloadWithoutTXTNormalization(ep, target, c.zone)
			if rawErr != nil {
				return rawErr
			}
			if rawPayload.Name != payload.Name {
				record, ok, err = c.findRecord(ctx, rawPayload)
				if err != nil {
					return err
				}
			}
		}
		if !ok {
			log.Printf("record not found for delete: %s %s -> %s", payload.Type, ep.DNSName, payload.Content)
			continue
		}
		log.Printf("deleting %s %s -> %s", payload.Type, ep.DNSName, payload.Content)
		if err := c.deleteRecord(ctx, record.ID); err != nil {
			return err
		}
	}
	return nil
}

func endpointToPayload(ep endpoint, target, zone, txtPrefix string) (recordPayload, error) {
	return endpointToPayloadWithTXTNormalization(ep, target, zone, txtPrefix, true)
}

func endpointToPayloadWithoutTXTNormalization(ep endpoint, target, zone string) (recordPayload, error) {
	return endpointToPayloadWithTXTNormalization(ep, target, zone, "", false)
}

func endpointToPayloadWithTXTNormalization(ep endpoint, target, zone, txtPrefix string, normalizeTXT bool) (recordPayload, error) {
	name, err := relativeName(ep.DNSName, zone)
	if err != nil {
		return recordPayload{}, err
	}
	if normalizeTXT && ep.RecordType == "TXT" {
		name = normalizeSRVTXTName(name, txtPrefix)
	}
	if normalizeTXT && ep.RecordType == "TXT" && ep.SetIdentifier != "" {
		target = withTXTSetIdentifier(target, ep.SetIdentifier)
	}

	payload := recordPayload{
		Name:    name,
		Type:    ep.RecordType,
		Content: target,
	}
	if ep.RecordTTL > 0 {
		payload.TTL = &ep.RecordTTL
	}

	if ep.RecordType == "SRV" {
		srv, err := parseSRVTarget(target)
		if err != nil {
			return recordPayload{}, err
		}
		payload.Priority = &srv.Priority
		payload.Content = fmt.Sprintf("%d %d %s", srv.Weight, srv.Port, strings.TrimSuffix(srv.Host, "."))
	}

	return payload, nil
}

func normalizeSRVTXTName(name, txtPrefix string) string {
	if txtPrefix == "" {
		return name
	}
	return strings.Replace(name, txtPrefix+"srv-", txtPrefix, 1)
}

func srvSetIdentifier(target string) string {
	srv, err := parseSRVTarget(target)
	if err != nil {
		return ""
	}
	host := strings.TrimSuffix(srv.Host, ".")
	if host == "" {
		return ""
	}
	return strings.Split(host, ".")[0]
}

func txtSetIdentifier(target string) string {
	labels := strings.Split(unquoteTXTTarget(target), ",")
	for _, label := range labels {
		key, value, ok := strings.Cut(label, "=")
		if ok && key == txtSetIdentifierLabel {
			return value
		}
	}
	return ""
}

func withTXTSetIdentifier(target, setIdentifier string) string {
	wasQuoted := strings.HasPrefix(target, "\"") && strings.HasSuffix(target, "\"")
	content := unquoteTXTTarget(target)
	if txtSetIdentifier(content) != "" {
		return target
	}
	content += "," + txtSetIdentifierLabel + "=" + setIdentifier
	if wasQuoted {
		return `"` + content + `"`
	}
	return content
}

func unquoteTXTTarget(target string) string {
	return strings.Trim(strings.TrimSpace(target), `"`)
}

func relativeName(dnsName, zone string) (string, error) {
	dnsName = strings.TrimSuffix(dnsName, ".")
	zone = strings.TrimSuffix(zone, ".")
	if dnsName == zone {
		return "", nil
	}
	suffix := "." + zone
	if !strings.HasSuffix(dnsName, suffix) {
		return "", fmt.Errorf("dnsName %q is outside zone %q", dnsName, zone)
	}
	return strings.TrimSuffix(dnsName, suffix), nil
}

type srvTarget struct {
	Priority int
	Weight   int
	Port     int
	Host     string
}

func parseSRVTarget(target string) (srvTarget, error) {
	parts := strings.Fields(strings.TrimSpace(target))
	if len(parts) != 4 {
		return srvTarget{}, fmt.Errorf("invalid SRV target %q: expected priority weight port host", target)
	}

	priority, err := strconv.Atoi(parts[0])
	if err != nil {
		return srvTarget{}, fmt.Errorf("invalid SRV priority in %q: %w", target, err)
	}
	weight, err := strconv.Atoi(parts[1])
	if err != nil {
		return srvTarget{}, fmt.Errorf("invalid SRV weight in %q: %w", target, err)
	}
	port, err := strconv.Atoi(parts[2])
	if err != nil {
		return srvTarget{}, fmt.Errorf("invalid SRV port in %q: %w", target, err)
	}

	return srvTarget{
		Priority: priority,
		Weight:   weight,
		Port:     port,
		Host:     parts[3],
	}, nil
}

func (c *dnsimpleClient) whoami(ctx context.Context) (string, error) {
	var out whoamiResponse
	if err := c.do(ctx, http.MethodGet, "/whoami", nil, &out); err != nil {
		return "", err
	}
	if out.Data.Account.ID == 0 {
		return "", errors.New("whoami response did not include account id")
	}
	return strconv.FormatInt(out.Data.Account.ID, 10), nil
}

func (c *dnsimpleClient) listRecords(ctx context.Context, name string) ([]dnsimpleRecord, error) {
	var records []dnsimpleRecord
	for page := 1; ; page++ {
		path := fmt.Sprintf("/%s/zones/%s/records?page=%d", c.accountID, c.zone, page)
		if name != "" {
			path += "&name=" + urlQueryEscape(name)
		}

		var out listRecordsResponse
		if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
			return nil, err
		}
		records = append(records, out.Data...)

		if out.Pagination.TotalPages == 0 || page >= out.Pagination.TotalPages {
			break
		}
	}
	return records, nil
}

func (c *dnsimpleClient) findRecord(ctx context.Context, payload recordPayload) (dnsimpleRecord, bool, error) {
	records, err := c.listRecords(ctx, payload.Name)
	if err != nil {
		return dnsimpleRecord{}, false, err
	}

	for _, record := range records {
		if record.Type != payload.Type || record.Name != payload.Name || record.Content != payload.Content {
			continue
		}
		if payload.Type == "SRV" {
			if payload.Priority == nil || record.Priority == nil || *record.Priority != *payload.Priority {
				continue
			}
		}
		return record, true, nil
	}
	return dnsimpleRecord{}, false, nil
}

func (c *dnsimpleClient) createRecord(ctx context.Context, payload recordPayload) error {
	path := fmt.Sprintf("/%s/zones/%s/records", c.accountID, c.zone)
	return c.do(ctx, http.MethodPost, path, payload, nil)
}

func (c *dnsimpleClient) deleteRecord(ctx context.Context, id int64) error {
	path := fmt.Sprintf("/%s/zones/%s/records/%d", c.accountID, c.zone, id)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func (c *dnsimpleClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed with %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}

	return json.Unmarshal(respBody, out)
}

func urlQueryEscape(value string) string {
	replacer := strings.NewReplacer(
		" ", "%20",
		"#", "%23",
		"%", "%25",
		"&", "%26",
		"+", "%2B",
		"=", "%3D",
		"?", "%3F",
	)
	return replacer.Replace(value)
}
