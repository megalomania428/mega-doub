package config

// SourceProblems exposes sourceProblems to the external tests of the package.
func (l *Loaded) SourceProblems(i, j int, problems []Problem) []Problem {
	return l.sourceProblems(i, j, problems)
}
