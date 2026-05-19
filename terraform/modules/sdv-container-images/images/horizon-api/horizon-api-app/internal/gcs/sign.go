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
//
package gcs

import (
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"net/url"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	iamcredentials "google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/option"
)

// SignedGETURL returns a V4 signed GET URL for object. serviceAccountEmail is the IAM identity
// whose key signs the URL (Workload Identity / GSA with roles/iam.serviceAccountTokenCreator as needed).
// downloadName sets response-content-disposition when non-empty (safe filename for browsers).
func SignedGETURL(ctx context.Context, serviceAccountEmail, bucket, object string, expires time.Time, downloadName string) (string, error) {
	serviceAccountEmail = strings.TrimSpace(serviceAccountEmail)
	if serviceAccountEmail == "" {
		return "", fmt.Errorf("signing service account email is empty")
	}
	svc, err := iamcredentials.NewService(ctx, option.WithScopes(iamcredentials.CloudPlatformScope))
	if err != nil {
		return "", fmt.Errorf("iamcredentials client: %w", err)
	}
	saResource := fmt.Sprintf("projects/-/serviceAccounts/%s", serviceAccountEmail)
	signBytes := func(b []byte) ([]byte, error) {
		req := &iamcredentials.SignBlobRequest{
			Payload: base64.StdEncoding.EncodeToString(b),
		}
		resp, err := svc.Projects.ServiceAccounts.SignBlob(saResource, req).Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		sig, err := base64.StdEncoding.DecodeString(resp.SignedBlob)
		if err != nil {
			return nil, fmt.Errorf("decode SignBlob response: %w", err)
		}
		return sig, nil
	}

	opts := &storage.SignedURLOptions{
		Scheme:         storage.SigningSchemeV4,
		Method:         "GET",
		GoogleAccessID: serviceAccountEmail,
		SignBytes:      signBytes,
		Expires:        expires,
	}
	if dn := strings.TrimSpace(downloadName); dn != "" {
		cd := mime.FormatMediaType("attachment", map[string]string{"filename": dn})
		if cd != "" {
			q := url.Values{}
			q.Set("response-content-disposition", cd)
			opts.QueryParameters = q
		}
	}
	return storage.SignedURL(bucket, object, opts)
}
