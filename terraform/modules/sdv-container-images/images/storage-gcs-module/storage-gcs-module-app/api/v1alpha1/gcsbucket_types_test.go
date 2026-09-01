// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package v1alpha1

import "testing"

func TestDeleteGCPBucketOnCRDelete(t *testing.T) {
	if (GCSBucketSpec{}).DeleteGCPBucketOnCRDelete() {
		t.Fatal("omitted policy must Retain")
	}
	if (GCSBucketSpec{DeletionPolicy: DeletionPolicyRetain}).DeleteGCPBucketOnCRDelete() {
		t.Fatal("Retain must not delete GCP bucket")
	}
	if !(GCSBucketSpec{DeletionPolicy: DeletionPolicyDelete}).DeleteGCPBucketOnCRDelete() {
		t.Fatal("Delete must delete GCP bucket")
	}
	if !(GCSBucketSpec{DeletionPolicy: "delete"}).DeleteGCPBucketOnCRDelete() {
		t.Fatal("delete is case-insensitive")
	}
}
