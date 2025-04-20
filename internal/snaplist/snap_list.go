/*
Package snaplist provides the structure for the
image definition that will be parsed from a YAML file.
*/
package snaplist

// SnapList is the parent struct for the data
// contained within a classic image definition file
type SnapList struct {
	Architecture string  `yaml:"architecture"    json:"Architecture"`
	Series       string  `yaml:"series"          json:"Series"`
	Snaps        []*Snap `yaml:"snaps"           json:"Snaps"`
}

// Snap contains information about snaps
type Snap struct {
	SnapName     string `yaml:"name"     json:"SnapName"`
	SnapRevision int    `yaml:"revision" json:"SnapRevision,omitempty" jsonschema:"type=integer"`
	Store        string `yaml:"store"    json:"Store"                  default:"canonical"`
	Channel      string `yaml:"channel"  json:"Channel"                default:"stable"`
}
