package domain

import "math"

// earthRadiusKm is the mean radius of the earth, which is all a delivery distance needs.
const earthRadiusKm = 6371.0

// DistanceKm is the great-circle distance between two points, in kilometres (the haversine
// formula). Straight-line, not by road: it is what "does this restaurant deliver to me" is asked with.
func DistanceKm(lat1, lng1, lat2, lng2 float64) float64 {
	rad := func(deg float64) float64 { return deg * math.Pi / 180 }
	dLat, dLng := rad(lat2-lat1), rad(lng2-lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return earthRadiusKm * 2 * math.Asin(math.Sqrt(math.Min(1, a)))
}

// ValidPoint says whether these are coordinates that exist: latitude -90 to 90, longitude -180 to 180.
func ValidPoint(lat, lng float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lng) && lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}
