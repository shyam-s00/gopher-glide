package httpreader

import "regexp"

// journeyVarPattern matches {{varName}} placeholders used for actor-memory
// variable injection. Dynamic placeholders such as {{$uuid}}, {{$timestamp}},
// and {{$randomInt}} start with `$` — not a word character — so they never
// match here and are correctly excluded from chain detection: they are
// generated fresh per-request and never depend on a prior @gg-export.
var journeyVarPattern = regexp.MustCompile(`\{\{(\w+)\}\}`)

// referencedVars returns the set of {{varName}} placeholder names referenced
// anywhere in the spec's URL, body, and header values.
func referencedVars(spec RequestSpec) map[string]bool {
	vars := make(map[string]bool)
	scan := func(s string) {
		for _, m := range journeyVarPattern.FindAllStringSubmatch(s, -1) {
			vars[m[1]] = true
		}
	}
	scan(spec.URL)
	scan(spec.Body)
	for _, vv := range spec.Headers {
		for _, v := range vv {
			scan(v)
		}
	}
	return vars
}

// GroupJourneys applies "Smart Detection" to an ordered slice of RequestSpecs,
// grouping contiguous runs that are linked by an @gg-export → {{var}} chain
// into a single stateful Journey. Every other request becomes its own
// independent single-step Journey — so a purely stateless .http file produces
// exactly one Journey per request, round-robin'd just like before.
//
// Heuristic, applied in declaration order:
//  1. A spec carrying its own `# @gg-export` directive(s) starts (or
//     continues) the active Stateful Block; its exported names join the
//     block's tracked set.
//  2. A spec that references ({{varName}}) any name already in the tracked
//     set joins the active Block.
//  3. Otherwise the spec is independent: the active Block (if any, including
//     a single exporter with no consumer) is closed first, and the spec
//     becomes its own single-step Journey.
func GroupJourneys(specs []RequestSpec) []Journey {
	journeys := make([]Journey, 0, len(specs))

	var block []RequestSpec
	tracked := make(map[string]bool)

	flushBlock := func() {
		if len(block) == 0 {
			return
		}
		journeys = append(journeys, Journey{Specs: block})
		block = nil
		tracked = make(map[string]bool)
	}

	for _, spec := range specs {
		hasExports := len(spec.Exports) > 0

		joinsChain := false
		if len(tracked) > 0 {
			for name := range referencedVars(spec) {
				if tracked[name] {
					joinsChain = true
					break
				}
			}
		}

		if hasExports || joinsChain {
			block = append(block, spec)
			for _, d := range spec.Exports {
				tracked[d.VarName] = true
			}
			continue
		}

		flushBlock()
		journeys = append(journeys, Journey{Specs: []RequestSpec{spec}})
	}

	flushBlock()
	return journeys
}

// Flatten concatenates every Journey's Specs back into a single ordered
// slice, preserving declaration order. It bridges callers that have not yet
// been migrated to execute Journeys directly — they continue to see the same
// flat, ordered request list that ParseFile returned before Smart Detection.
func Flatten(journeys []Journey) []RequestSpec {
	total := 0
	for _, j := range journeys {
		total += len(j.Specs)
	}
	specs := make([]RequestSpec, 0, total)
	for _, j := range journeys {
		specs = append(specs, j.Specs...)
	}
	return specs
}
