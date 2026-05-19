// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package invoke

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PostArgoEventsWebhook posts the workflow-dispatch JSON body to the Argo Events EventSource.
func PostArgoEventsWebhook(ctx context.Context, url string, body map[string]interface{}) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Keep below common gateway / browser timeouts so submit callers get a clear failure instead of hanging.
	client := &http.Client{Timeout: 25 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		msg := strings.TrimSpace(string(snippet))
		if len(msg) > 512 {
			msg = msg[:512] + "…"
		}
		if msg == "" {
			return fmt.Errorf("events webhook: HTTP %d", res.StatusCode)
		}
		return fmt.Errorf("events webhook: HTTP %d: %s", res.StatusCode, msg)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}
