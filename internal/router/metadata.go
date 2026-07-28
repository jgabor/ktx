package router

// SubcommandFromInput extracts the first non-flag token after the command
// binary. It returns "" when the input has no usable subcommand.
func SubcommandFromInput(input string) string {
	subcommand, _ := subcommandFromInput(input)
	return subcommand
}
