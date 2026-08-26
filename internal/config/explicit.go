package config

// LoadFile loads one explicit complete configuration without consulting the
// user configuration path.
func LoadFile(path string) (*Config, error) {
	return loadFile(path)
}
