package domain

import "time"

// Definitions is the single-source registry of every known platform setting.
// Adding a new setting is a one-liner append here — no migration, no new
// column.
var Definitions = []Definition{
	{
		Key:     "lifecycle.idle_stop_threshold",
		Kind:    KindDuration,
		Default: "1h",
		Validate: func(v string) error {
			d, err := time.ParseDuration(v)
			if err != nil {
				return ErrInvalidValue
			}
			if d < 0 {
				return ErrInvalidValue
			}
			return nil
		},
	},
	{
		Key:     "lifecycle.idle_removal_threshold",
		Kind:    KindDuration,
		Default: "168h", // 7 days
		Validate: func(v string) error {
			d, err := time.ParseDuration(v)
			if err != nil {
				return ErrInvalidValue
			}
			if d < 0 {
				return ErrInvalidValue
			}
			return nil
		},
	},
	{
		Key:     "lifecycle.abandoned_cleanup_threshold",
		Kind:    KindDuration,
		Default: "720h", // 30 days
		Validate: func(v string) error {
			d, err := time.ParseDuration(v)
			if err != nil {
				return ErrInvalidValue
			}
			if d <= 0 {
				return ErrInvalidValue
			}
			return nil
		},
	},
}

// DefinitionByKey returns the Definition registered under key, or nil.
func DefinitionByKey(key Key) *Definition {
	for i := range Definitions {
		if Definitions[i].Key == key {
			return &Definitions[i]
		}
	}
	return nil
}
