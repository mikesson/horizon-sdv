// Copyright (c) 2024-2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controller

import "sync"

// ModuleOpsMutex serializes module lifecycle transactions that mutate
// ModuleManagerState, Argo CD Applications, and dependent soft-feature state.
// Hold this mutex for the full duration of top-level enable/disable/auto-disable
// operations to prevent race conditions between API handlers and reconcilers.
var ModuleOpsMutex sync.Mutex
