// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandlerOnlyHealth(t *testing.T) {
	s := &Server{}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	s.HealthHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("health status=%d", rr.Code)
	}
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/ready", nil)
	s.HealthHandler().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("ready on health mux status=%d", rr2.Code)
	}
}

func TestListenConfigFromEnv(t *testing.T) {
	t.Setenv("API_TLS_CERT_FILE", "")
	t.Setenv("API_TLS_KEY_FILE", "")
	cfg := ListenConfigFromEnv()
	if cfg.TLSEnabled() {
		t.Fatal("expected tls disabled")
	}
	t.Setenv("API_TLS_CERT_FILE", "/tmp/cert")
	t.Setenv("API_TLS_KEY_FILE", "/tmp/key")
	cfg = ListenConfigFromEnv()
	if !cfg.TLSEnabled() {
		t.Fatal("expected tls enabled")
	}
}
