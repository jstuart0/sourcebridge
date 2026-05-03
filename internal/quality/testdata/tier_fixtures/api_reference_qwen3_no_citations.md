# API Reference

## ParseConfig

The ParseConfig function accepts a raw byte slice and returns a validated Config struct.
It validates the input against the schema and returns an error when required fields are
missing. The parser reads the TOML structure sequentially and stores each decoded key in
the corresponding Config field. When an unknown key is encountered, the function returns
an error describing the unrecognized field.

## Resolve

The Resolve method accepts a context and a set of environment variables. It returns the
resolved configuration or an error when resolution fails. The resolver checks each field
in declaration order and ensures that variable interpolation completes before returning.
It calls the registered validator chain and guarantees that all constraints are satisfied
before the Config is returned to the caller.

## Validate

The Validate function accepts a Config value and returns a slice of ValidationError values.
It checks each field against its declared constraints and ensures that dependent fields are
consistent with one another. When a field receives an invalid value, the function stores a
descriptive error in the result slice. The function returns nil when all constraints are
satisfied.

## WriteDefaults

The WriteDefaults function accepts an io.Writer and writes a TOML document containing
default values for every Config field. It sends one line per field, with a comment
describing the valid range. The function returns the number of bytes written and any
write error propagated from the underlying writer.
