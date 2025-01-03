package htmx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var (
	DefaultTemplateFuncs = template.FuncMap{}
	UseTemplateCache     = true
	templateCache        = sync.Map{} // Cache for parsed templates
)

type (
	RenderableComponent interface {
		Render(ctx context.Context) (template.HTML, error)
		Wrap(renderer RenderableComponent, target string) RenderableComponent
		With(r RenderableComponent, target string) RenderableComponent
		Attach(target string) RenderableComponent
		SetData(input map[string]any) RenderableComponent
		AddData(key string, value any) RenderableComponent
		SetGlobalData(input map[string]any) RenderableComponent
		AddGlobalData(key string, value any) RenderableComponent
		AddTemplateFunction(name string, function interface{}) RenderableComponent
		AddTemplateFunctions(funcs template.FuncMap) RenderableComponent
		SetURL(url *url.URL)
		Reset() *Component

		data() map[string]any
		injectData(input map[string]any)
		injectGlobalData(input map[string]any)
		addPartial(key string, value any)
		partials() map[string]RenderableComponent
		isWrapped() bool
		wrapper() RenderableComponent
		target() string
	}

	Component struct {
		templateData    map[string]any
		with            map[string]RenderableComponent
		partial         map[string]any
		globalData      map[string]any
		wrappedRenderer RenderableComponent
		wrappedTarget   string
		templates       []string
		url             *url.URL
		functions       template.FuncMap
		fs              fs.FS
	}
)

func NewComponent(templates ...string) *Component {
	return &Component{
		templateData: make(map[string]any),
		functions:    make(template.FuncMap),
		partial:      make(map[string]any),
		with:         make(map[string]RenderableComponent),
		templates:    templates,
		fs:           os.DirFS("./"),
	}
}

// FS sets the filesystem to load templates from, this allows for embedding templates into the go binary.
func (c *Component) FS(fsys fs.FS) *Component {
	c.fs = fsys
	return c
}

// Render renders the given templates with the given data
// it has all the default template functions and the additional template functions
// that are added with AddTemplateFunction
func (c *Component) Render(ctx context.Context) (template.HTML, error) {
	// Check for circular references
	if ctx.Value(c) != nil {
		return "", errors.New("circular reference detected in partials")
	}

	// Add current component to context
	ctx = context.WithValue(ctx, c, true)

	for key, value := range c.partials() {
		value.injectData(c.templateData)
		value.injectGlobalData(c.globalData)

		ch, err := value.Render(ctx)
		if err != nil {
			return "", err
		}
		c.addPartial(key, ch)
	}

	//get the name of the first template file
	if len(c.templates) == 0 {
		return "", errors.New("no templates provided for rendering")
	}

	return c.renderNamed(ctx, filepath.Base(c.templates[0]), c.templates, c.templateData)
}

// renderNamed renders the given templates with the given data
// it has all the default template functions and the additional template functions
// that are added with AddTemplateFunction
func (c *Component) renderNamed(ctx context.Context, name string, templates []string, input map[string]any) (template.HTML, error) {
	if len(templates) == 0 {
		return "", nil
	}

	var err error
	functions := make(template.FuncMap)
	for key, value := range DefaultTemplateFuncs {
		functions[key] = value
	}

	if c.functions != nil {
		for key, value := range c.functions {
			functions[key] = value
		}
	}

	cacheKey := generateCacheKey(templates, functions)
	tmpl, cached := templateCache.Load(cacheKey)
	if !cached || !UseTemplateCache {
		// Parse and cache template as before
		tmpl, err = template.New(name).Funcs(functions).ParseFS(c.fs, templates...)
		if err != nil {
			return "", err
		}
		templateCache.Store(cacheKey, tmpl)
	}

	data := struct {
		Ctx      context.Context
		Data     map[string]any
		Global   map[string]any
		Partials map[string]any
		URL      *url.URL
	}{
		Ctx:      ctx,
		Data:     input,
		Global:   c.globalData,
		Partials: c.partial,
		URL:      c.url,
	}

	if t, ok := tmpl.(*template.Template); ok {
		var buf bytes.Buffer
		err = t.Execute(&buf, data)
		if err != nil {
			return "", err
		}

		return template.HTML(buf.String()), nil // Return rendered content
	}

	return "", errors.New("template is not a *template.Template")
}

// Wrap wraps the component with the given renderer
func (c *Component) Wrap(renderer RenderableComponent, target string) RenderableComponent {
	c.wrappedRenderer = renderer
	c.wrappedTarget = target

	return c
}

// With adds a partial to the component
func (c *Component) With(r RenderableComponent, target string) RenderableComponent {
	if c.with == nil {
		c.with = make(map[string]RenderableComponent)
	}

	if c.url != nil {
		r.SetURL(c.url)
	}

	c.with[target] = r

	return c
}

// Attach adds a template to the main component but doesn't pre-render it
func (c *Component) Attach(target string) RenderableComponent {
	if c.templates == nil {
		c.templates = make([]string, 0)
	}

	c.templates = append(c.templates, target)
	return c
}

func (c *Component) AddTemplateFunction(name string, function interface{}) RenderableComponent {
	if c.functions == nil {
		c.functions = make(template.FuncMap)
	}

	c.functions[name] = function

	return c
}

func (c *Component) AddTemplateFunctions(funcs template.FuncMap) RenderableComponent {
	if c.functions == nil {
		c.functions = make(template.FuncMap)
	}

	for key, value := range funcs {
		c.functions[key] = value
	}

	return c
}

func (c *Component) SetGlobalData(input map[string]any) RenderableComponent {
	if c.globalData == nil {
		c.globalData = make(map[string]any)
	}

	c.globalData = input

	return c
}

func (c *Component) AddGlobalData(key string, value any) RenderableComponent {
	if c.globalData == nil {
		c.globalData = make(map[string]any)
	}

	c.globalData[key] = value

	return c
}

// SetData adds data to the component
func (c *Component) SetData(input map[string]any) RenderableComponent {
	if c.templateData == nil {
		c.templateData = make(map[string]any)
	}

	c.templateData = input

	return c
}

func (c *Component) AddData(key string, value any) RenderableComponent {
	if c.templateData == nil {
		c.templateData = make(map[string]any)
	}

	c.templateData[key] = value

	return c
}

func (c *Component) SetURL(url *url.URL) {
	c.url = url

	// Recursively set the URL for all partials
	for _, partial := range c.with {
		partial.SetURL(url)
	}
}

// isWrapped returns true if the component is wrapped
func (c *Component) isWrapped() bool {
	return c.wrappedRenderer != nil
}

// wrapper returns the wrapped renderer
func (c *Component) wrapper() RenderableComponent {
	return c.wrappedRenderer
}

// target returns the target
func (c *Component) target() string {
	return c.wrappedTarget
}

// partials returns the partials
func (c *Component) partials() map[string]RenderableComponent {
	return c.with
}

// injectData injects the input data into the template data
func (c *Component) injectData(input map[string]any) {
	for key, value := range input {
		if _, ok := c.templateData[key]; !ok {
			c.templateData[key] = value
		}
	}
}

func (c *Component) injectGlobalData(input map[string]any) {
	if c.globalData == nil {
		c.globalData = make(map[string]any)
	}

	for key, value := range input {
		if _, ok := c.globalData[key]; !ok {
			c.globalData[key] = value
		}
	}
}

// addPartial adds a partial to the component
func (c *Component) addPartial(key string, value any) {
	c.partial[key] = value
}

// data returns the template data
func (c *Component) data() map[string]any {
	return c.templateData
}

func (c *Component) Reset() *Component {
	c.templateData = make(map[string]any)
	c.globalData = make(map[string]any)
	c.partial = make(map[string]any)
	c.with = make(map[string]RenderableComponent)
	c.url = nil

	return c
}

// Generate a hash of the function names to include in the cache key
func generateCacheKey(templates []string, funcs template.FuncMap) string {
	var funcNames []string
	for name := range funcs {
		funcNames = append(funcNames, name)
	}
	// Sort function names to ensure consistent ordering
	sort.Strings(funcNames)
	hash := sha256.Sum256([]byte(strings.Join(funcNames, ",")))
	return templates[0] + ":" + hex.EncodeToString(hash[:])
}
