// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gcecatalogfeature

import "math"

// regionCoordinates approximates each GCE region's location for the ranking's
// proximity term. GCE publishes no coordinates; the region list is small and
// stable, so a static table in code suffices. An unknown region scores
// neutrally rather than failing the ranking.
var regionCoordinates = map[string]struct{ lat, lon float64 }{
	"africa-south1":           {-26.2, 28.0},  // Johannesburg
	"asia-east1":              {24.1, 120.5},  // Changhua County
	"asia-east2":              {22.3, 114.2},  // Hong Kong
	"asia-northeast1":         {35.7, 139.7},  // Tokyo
	"asia-northeast2":         {34.7, 135.5},  // Osaka
	"asia-northeast3":         {37.6, 127.0},  // Seoul
	"asia-south1":             {19.1, 72.9},   // Mumbai
	"asia-south2":             {28.6, 77.2},   // Delhi
	"asia-southeast1":         {1.35, 103.8},  // Singapore
	"asia-southeast2":         {-6.2, 106.8},  // Jakarta
	"australia-southeast1":    {-33.9, 151.2}, // Sydney
	"australia-southeast2":    {-37.8, 145.0}, // Melbourne
	"europe-central2":         {52.2, 21.0},   // Warsaw
	"europe-north1":           {60.6, 27.2},   // Hamina
	"europe-north2":           {59.3, 18.1},   // Stockholm
	"europe-southwest1":       {40.4, -3.7},   // Madrid
	"europe-west1":            {50.5, 3.8},    // St. Ghislain
	"europe-west2":            {51.5, -0.1},   // London
	"europe-west3":            {50.1, 8.7},    // Frankfurt
	"europe-west4":            {53.4, 6.8},    // Eemshaven
	"europe-west6":            {47.4, 8.5},    // Zurich
	"europe-west8":            {45.5, 9.2},    // Milan
	"europe-west9":            {48.9, 2.4},    // Paris
	"europe-west10":           {52.5, 13.4},   // Berlin
	"europe-west12":           {45.1, 7.7},    // Turin
	"me-central1":             {25.3, 51.5},   // Doha
	"me-central2":             {26.4, 50.1},   // Dammam
	"me-west1":                {32.1, 34.8},   // Tel Aviv
	"northamerica-northeast1": {45.5, -73.6},  // Montreal
	"northamerica-northeast2": {43.7, -79.4},  // Toronto
	"northamerica-south1":     {20.6, -100.4}, // Queretaro
	"southamerica-east1":      {-23.5, -46.6}, // Sao Paulo
	"southamerica-west1":      {-33.4, -70.7}, // Santiago
	"us-central1":             {41.3, -95.9},  // Council Bluffs
	"us-east1":                {33.2, -80.0},  // Moncks Corner
	"us-east4":                {39.0, -77.5},  // Ashburn
	"us-east5":                {40.0, -83.0},  // Columbus
	"us-south1":               {32.8, -96.8},  // Dallas
	"us-west1":                {45.6, -121.2}, // The Dalles
	"us-west2":                {34.1, -118.2}, // Los Angeles
	"us-west3":                {40.8, -111.9}, // Salt Lake City
	"us-west4":                {36.2, -115.1}, // Las Vegas
}

// regionDistanceKm returns the great-circle distance between two regions and
// whether both are in the coordinate table.
func regionDistanceKm(a, b string) (float64, bool) {
	ca, okA := regionCoordinates[a]
	cb, okB := regionCoordinates[b]
	if !okA || !okB {
		return 0, false
	}
	const earthRadiusKm = 6371.0
	toRad := func(deg float64) float64 { return deg * math.Pi / 180 }
	dLat := toRad(cb.lat - ca.lat)
	dLon := toRad(cb.lon - ca.lon)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(ca.lat))*math.Cos(toRad(cb.lat))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusKm * math.Asin(math.Sqrt(h)), true
}
