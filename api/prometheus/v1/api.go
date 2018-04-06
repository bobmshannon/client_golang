// Copyright 2017 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build go1.7

// Package v1 provides bindings to the Prometheus HTTP API v1:
// http://prometheus.io/docs/querying/api/
package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/api"
	"github.com/prometheus/common/model"
)

const (
	statusAPIError = 422

	apiPrefix = "/api/v1"

	epAlertManagers   = apiPrefix + "/alertmanagers"
	epQuery           = apiPrefix + "/query"
	epQueryRange      = apiPrefix + "/query_range"
	epLabelValues     = apiPrefix + "/label/:name/values"
	epSeries          = apiPrefix + "/series"
	epTargets         = apiPrefix + "/targets"
	epSnapshot        = apiPrefix + "/admin/tsdb/snapshot"
	epDeleteSeries    = apiPrefix + "/admin/tsdb/delete_series"
	epCleanTombstones = apiPrefix + "/admin/tsdb/clean_tombstones"
	epConfig          = apiPrefix + "/status/config"
	epFlags           = apiPrefix + "/status/flags"
)

// ErrorType models the different API error types.
type ErrorType string

// Possible values for ErrorType.
const (
	ErrBadData     ErrorType = "bad_data"
	ErrTimeout               = "timeout"
	ErrCanceled              = "canceled"
	ErrExec                  = "execution"
	ErrBadResponse           = "bad_response"

	HealthUp      = "up"
	HealthUnknown = "unknown"
	HealthDown    = "down"
)

// Error is an error returned by the API.
type Error struct {
	Type ErrorType
	Msg  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Type, e.Msg)
}

// Range represents a sliced time range.
type Range struct {
	// The boundaries of the time range.
	Start, End time.Time
	// The maximum time between two slices within the boundaries.
	Step time.Duration
}

// API provides bindings for Prometheus's v1 API.
type API interface {
	// Query performs a query for the given time.
	Query(ctx context.Context, query string, ts time.Time) (model.Value, error)
	// QueryRange performs a query for the given range.
	QueryRange(ctx context.Context, query string, r Range) (model.Value, error)
	// LabelValues performs a query for the values of the given label.
	LabelValues(ctx context.Context, label string) (model.LabelValues, error)
	// Series finds series by label matchers.
	Series(ctx context.Context, matches []string, startTime time.Time, endTime time.Time) ([]model.LabelSet, error)
	// Targets returns an overview of the current state of the Prometheus target discovery.
	Targets(ctx context.Context) (TargetsResult, error)
	// AlertManagers returns an overview of the current state of the Prometheus alertmanager discovery.
	AlertManagers(ctx context.Context) (AlertManagersResult, error)
}

type AdminAPI interface {
	// Snapshot creates a snapshot of all current data into snapshots/<datetime>-<rand>
	// under the TSDB's data directory and returns the directory as response.
	Snapshot(ctx context.Context, skipHead bool) (SnapshotResult, error)
	// DeleteSeries deletes data for a selection of series in a time range.
	DeleteSeries(ctx context.Context, matches []string, startTime time.Time, endTime time.Time) error
	// CleanTombstones removes the deleted data from disk and cleans up the existing tombstones.
	CleanTombstones(ctx context.Context) error
}

type StatusAPI interface {
	// Config returns the current Prometheus configuration.
	Config(ctx context.Context) (ConfigResult, error)
	// Flags returns the flag values that Prometheus was launched with.
	Flags(ctx context.Context) (FlagsResult, error)
}

// queryResult contains result data for a query.
type queryResult struct {
	Type   model.ValueType `json:"resultType"`
	Result interface{}     `json:"result"`

	// The decoded value.
	v model.Value
}

type AlertManagersResult struct {
	Active  []AlertManager `json:"activeAlertManagers"`
	Dropped []AlertManager `json:"droppedAlertManagers"`
}

type AlertManager struct {
	URL string `json:"url"`
}

type ConfigResult struct {
	YAML string `json:"yaml"`
}

type FlagsResult map[string]string

type SnapshotResult struct {
	Name string `json:"name"`
}

type TargetsResult struct {
	Active  []Target `json:"activeTargets"`
	Dropped []Target `json:"droppedTargets"`
}

type Target struct {
	DiscoveredLabels model.LabelSet `json:"discoveredLabels"`
	Labels           model.LabelSet `json:"labels"`
	ScrapeURL        string         `json:"scrapeUrl"`
	LastError        string         `json:"lastError"`
	LastScrape       time.Time      `json:"lastScrape"`
	Health           string         `json:"health"`
}

func (qr *queryResult) UnmarshalJSON(b []byte) error {
	v := struct {
		Type   model.ValueType `json:"resultType"`
		Result json.RawMessage `json:"result"`
	}{}

	err := json.Unmarshal(b, &v)
	if err != nil {
		return err
	}

	switch v.Type {
	case model.ValScalar:
		var sv model.Scalar
		err = json.Unmarshal(v.Result, &sv)
		qr.v = &sv

	case model.ValVector:
		var vv model.Vector
		err = json.Unmarshal(v.Result, &vv)
		qr.v = vv

	case model.ValMatrix:
		var mv model.Matrix
		err = json.Unmarshal(v.Result, &mv)
		qr.v = mv

	default:
		err = fmt.Errorf("unexpected value type %q", v.Type)
	}
	return err
}

// NewAPI returns a new API for the client.
//
// It is safe to use the returned API from multiple goroutines.
func NewAPI(c api.Client) API {
	return &httpAPI{client: apiClient{c}}
}

type httpAPI struct {
	client api.Client
}

// NewAdminAPI returns a new Admin API for the client.
func NewAdminAPI(c api.Client) AdminAPI {
	return &httpAdminAPI{client: apiClient{c}}
}

type httpAdminAPI struct {
	client api.Client
}

// NewStatusAPI returns a new Status API for the client.
func NewStatusAPI(c api.Client) StatusAPI {
	return &httpStatusAPI{client: apiClient{c}}
}

type httpStatusAPI struct {
	client api.Client
}

func (h *httpAPI) AlertManagers(ctx context.Context) (AlertManagersResult, error) {
	u := h.client.URL(epAlertManagers, nil)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return AlertManagersResult{}, err
	}

	_, body, err := h.client.Do(ctx, req)
	if err != nil {
		return AlertManagersResult{}, err
	}

	var res AlertManagersResult
	err = json.Unmarshal(body, &res)
	return res, err
}

func (h *httpAPI) Query(ctx context.Context, query string, ts time.Time) (model.Value, error) {
	u := h.client.URL(epQuery, nil)
	q := u.Query()

	q.Set("query", query)
	if !ts.IsZero() {
		q.Set("time", ts.Format(time.RFC3339Nano))
	}

	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	_, body, err := h.client.Do(ctx, req)
	if err != nil {
		return nil, err
	}

	var qres queryResult
	err = json.Unmarshal(body, &qres)

	return model.Value(qres.v), err
}

func (h *httpAPI) QueryRange(ctx context.Context, query string, r Range) (model.Value, error) {
	u := h.client.URL(epQueryRange, nil)
	q := u.Query()

	var (
		start = r.Start.Format(time.RFC3339Nano)
		end   = r.End.Format(time.RFC3339Nano)
		step  = strconv.FormatFloat(r.Step.Seconds(), 'f', 3, 64)
	)

	q.Set("query", query)
	q.Set("start", start)
	q.Set("end", end)
	q.Set("step", step)

	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	_, body, err := h.client.Do(ctx, req)
	if err != nil {
		return nil, err
	}

	var qres queryResult
	err = json.Unmarshal(body, &qres)

	return model.Value(qres.v), err
}

func (h *httpAPI) LabelValues(ctx context.Context, label string) (model.LabelValues, error) {
	u := h.client.URL(epLabelValues, map[string]string{"name": label})
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	_, body, err := h.client.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	var labelValues model.LabelValues
	err = json.Unmarshal(body, &labelValues)
	return labelValues, err
}

func (h *httpAPI) Series(ctx context.Context, matches []string, startTime time.Time, endTime time.Time) ([]model.LabelSet, error) {
	u := h.client.URL(epSeries, nil)
	q := u.Query()

	for _, m := range matches {
		q.Add("match[]", m)
	}

	q.Set("start", startTime.Format(time.RFC3339Nano))
	q.Set("end", endTime.Format(time.RFC3339Nano))

	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	_, body, err := h.client.Do(ctx, req)
	if err != nil {
		return nil, err
	}

	var mset []model.LabelSet
	err = json.Unmarshal(body, &mset)
	return mset, err
}

func (h *httpAdminAPI) Snapshot(ctx context.Context, skipHead bool) (SnapshotResult, error) {
	u := h.client.URL(epSnapshot, nil)
	q := u.Query()

	q.Set("skip_head", strconv.FormatBool(skipHead))

	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodPost, u.String(), nil)
	if err != nil {
		return SnapshotResult{}, err
	}

	_, body, err := h.client.Do(ctx, req)
	if err != nil {
		return SnapshotResult{}, err
	}

	var res SnapshotResult
	err = json.Unmarshal(body, &res)
	return res, err
}

func (h *httpAPI) Targets(ctx context.Context) (TargetsResult, error) {
	u := h.client.URL(epTargets, nil)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return TargetsResult{}, err
	}

	_, body, err := h.client.Do(ctx, req)
	if err != nil {
		return TargetsResult{}, err
	}

	var res TargetsResult
	err = json.Unmarshal(body, &res)
	return res, err
}

func (h *httpAdminAPI) DeleteSeries(ctx context.Context, matches []string, startTime time.Time, endTime time.Time) error {
	u := h.client.URL(epDeleteSeries, nil)
	q := u.Query()

	for _, m := range matches {
		q.Add("match[]", m)
	}

	q.Set("start", startTime.Format(time.RFC3339Nano))
	q.Set("end", endTime.Format(time.RFC3339Nano))

	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodPost, u.String(), nil)
	if err != nil {
		return err
	}

	_, _, err = h.client.Do(ctx, req)
	if err != nil {
		return err
	}

	return nil
}

func (h *httpAdminAPI) CleanTombstones(ctx context.Context) error {
	u := h.client.URL(epCleanTombstones, nil)

	req, err := http.NewRequest(http.MethodPost, u.String(), nil)
	if err != nil {
		return err
	}

	if _, _, err = h.client.Do(ctx, req); err != nil {
		return err
	}

	return nil
}

func (h *httpStatusAPI) Config(ctx context.Context) (ConfigResult, error) {
	u := h.client.URL(epConfig, nil)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return ConfigResult{}, err
	}

	_, body, err := h.client.Do(ctx, req)
	if err != nil {
		return ConfigResult{}, err
	}

	var res ConfigResult
	err = json.Unmarshal(body, &res)
	return res, err
}

func (h *httpStatusAPI) Flags(ctx context.Context) (FlagsResult, error) {
	u := h.client.URL(epFlags, nil)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return FlagsResult{}, err
	}

	_, body, err := h.client.Do(ctx, req)
	if err != nil {
		return FlagsResult{}, err
	}

	var res FlagsResult
	err = json.Unmarshal(body, &res)
	return res, err
}

// apiClient wraps a regular client and processes successful API responses.
// Successful also includes responses that errored at the API level.
type apiClient struct {
	api.Client
}

type apiResponse struct {
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data"`
	ErrorType ErrorType       `json:"errorType"`
	Error     string          `json:"error"`
}

func (c apiClient) Do(ctx context.Context, req *http.Request) (*http.Response, []byte, error) {
	resp, body, err := c.Client.Do(ctx, req)
	if err != nil {
		return resp, body, err
	}

	code := resp.StatusCode

	if code/100 != 2 && code != statusAPIError {
		return resp, body, &Error{
			Type: ErrBadResponse,
			Msg:  fmt.Sprintf("bad response code %d", resp.StatusCode),
		}
	}

	var result apiResponse

	if err = json.Unmarshal(body, &result); err != nil {
		return resp, body, &Error{
			Type: ErrBadResponse,
			Msg:  err.Error(),
		}
	}

	if (code == statusAPIError) != (result.Status == "error") {
		err = &Error{
			Type: ErrBadResponse,
			Msg:  "inconsistent body for response code",
		}
	}

	if code == statusAPIError && result.Status == "error" {
		err = &Error{
			Type: result.ErrorType,
			Msg:  result.Error,
		}
	}

	return resp, []byte(result.Data), err
}
