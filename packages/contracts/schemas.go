package contracts

import "embed"

// Schemas holds every JSON Schema under schemas/. The validate package
// compiles them; other code should go through validate rather than read them.
//
//go:embed schemas
var Schemas embed.FS
