package main

import (
	"regexp"
	"strings"
	"sync"
)

type PromptTemplate struct {
	Pattern    *regexp.Regexp
	Template   string
	ParamNames []string
	Response   []byte
	HitCount   int64
}

type TemplateMatcher struct {
	templates    []*PromptTemplate
	mu           sync.RWMutex
	maxTemplates int
}

func NewTemplateMatcher(maxTemplates int) *TemplateMatcher {
	return &TemplateMatcher{
		templates:    make([]*PromptTemplate, 0, maxTemplates),
		maxTemplates: maxTemplates,
	}
}

// extractTemplateFromPrompt tries to generalize a prompt by replacing
// numbers, quoted strings, and capitalized words with placeholders.
// e.g., "What is the weather in London?" becomes "What is the weather in {entity}?"
func extractTemplateFromPrompt(prompt string) (template string, params []string, extracted bool) {
	words := strings.Fields(prompt)
	if len(words) < 3 {
		return "", nil, false
	}

	paramNames := make([]string, 0)
	templateWords := make([]string, len(words))
	copy(templateWords, words)

	numberPattern := regexp.MustCompile(`^\d+([.,]\d+)?$`)
	quotedPattern := regexp.MustCompile(`^[""](.+?)[""]$`)
	capitalizedPattern := regexp.MustCompile(`^[A-Z][a-z]+`)

	hasVariable := false

	for i, w := range words {
		switch {
		case numberPattern.MatchString(w):
			templateWords[i] = "{num}"
			paramNames = append(paramNames, "num")
			hasVariable = true
		case quotedPattern.MatchString(w):
			templateWords[i] = "{str}"
			paramNames = append(paramNames, "str")
			hasVariable = true
		case capitalizedPattern.MatchString(w) && i > 0:
			templateWords[i] = "{entity}"
			paramNames = append(paramNames, "entity")
			hasVariable = true
		}
	}

	if !hasVariable || len(paramNames) == 0 {
		return "", nil, false
	}

	return strings.Join(templateWords, " "), paramNames, true
}

func (tm *TemplateMatcher) LearnFromPrompt(prompt string, response []byte) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	template, paramNames, ok := extractTemplateFromPrompt(prompt)
	if !ok {
		return
	}

	for _, t := range tm.templates {
		if t.Template == template {
			t.Response = response
			return
		}
	}

	if len(tm.templates) >= tm.maxTemplates {
		return
	}

	pattern := regexp.MustCompile(regexp.QuoteMeta(template))
	tm.templates = append(tm.templates, &PromptTemplate{
		Pattern:    pattern,
		Template:   template,
		ParamNames: paramNames,
		Response:   response,
	})
}

func (tm *TemplateMatcher) Match(prompt string) ([]byte, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	for _, t := range tm.templates {
		if t.Pattern.MatchString(prompt) {
			t.HitCount++
			return t.Response, true
		}
	}
	return nil, false
}
