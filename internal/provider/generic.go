// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

// A declarative REST resource for the many Temps endpoints that follow plain
// create/read/update/delete semantics. A gspec names the paths, the fields and
// how the server echoes them; genericResource does the Terraform plumbing,
// drift detection and import the same way for all of them.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/float64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

type fieldKind int

const (
	kString fieldKind = iota
	kInt
	kBool
	kFloat
	kStringList
	kStringSet
	kIntList
	kIntSet
	kStringMap
	kJSON // a JSON document held as a string and compared semantically
)

// gfield describes one attribute and where it lives in the API documents.
type gfield struct {
	Name string
	// JSON is the request and response key, dotted for nested objects
	// ("credentials.token"); empty means Name. "." (kJSON only) makes the
	// attribute the whole request document.
	JSON string
	// Read is the response key when it differs from JSON. "-" means the
	// server never returns the value: state keeps what was configured.
	Read string
	Kind fieldKind
	Desc string

	Required bool
	// Optional attributes are also computed (the server's value is kept when
	// left out) unless Clearable.
	Optional bool
	// Computed alone marks an output.
	Computed bool
	// Clearable: optional and not computed; removing it sends null.
	Clearable bool
	// ClearWith is sent instead of null when a Clearable value is removed,
	// for PATCH endpoints where null means "unchanged" and an empty string
	// clears. With "", an empty string read back is also treated as unset.
	ClearWith any
	Sensitive bool
	ForceNew  bool
	// CreateOnly is sent only on create and forces replacement when changed.
	CreateOnly bool
	// Initial is sent only on create; later changes are neither applied nor
	// force replacement (a starting password, starting roles).
	Initial bool
	// Path values only build request paths and are never sent in a body.
	Path bool
	// Volatile outputs change on their own (status, timestamps): they are not
	// carried over from state into plans.
	Volatile bool
	// Subset (kJSON): the server fills in defaults; keep the configured
	// document while every configured key still has the configured value.
	Subset bool
	OneOf  []string
	// Default for an optional string, bool or int left out of configuration.
	Default any
	// ZeroOK accepts 0 as an imported ID (the control-plane node is 0).
	ZeroOK bool
	// Time marks an RFC 3339 timestamp: a value naming the same instant as
	// the one held (another offset or precision) is not a change.
	Time bool
	// ReplaceIf (strings) forces replacement for the changes it accepts.
	ReplaceIf     stringplanmodifier.RequiresReplaceIfFunc
	ReplaceIfDesc string
}

func (f *gfield) key() string {
	if f.JSON != "" {
		return f.JSON
	}
	return f.Name
}

func (f *gfield) readKey() string {
	if f.Read != "" {
		return f.Read
	}
	return f.key()
}

func (f *gfield) writeOnly() bool { return f.Read == "-" }
func (f *gfield) output() bool    { return f.Computed && !f.Required && !f.Optional && !f.Clearable }

// gvals holds a resource's attribute values by name, "id" included.
type gvals map[string]attr.Value

// gop is one HTTP operation.
type gop struct {
	Method string
	// Path is a template: {attr} takes an attribute's value, {id} the
	// server identifier.
	Path   string
	PathFn func(v gvals, id string) string
	// Envelope is the key the object sits under in the response.
	Envelope string
	// Body is a fixed request body (a reset on delete).
	Body map[string]any
	// Query adds query parameters (an owning project the API scopes the
	// object by).
	Query func(v gvals) url.Values
}

func (o gop) query(v gvals) url.Values {
	if o.Query == nil {
		return nil
	}
	return o.Query(v)
}

type gspec struct {
	Name   string // resource type after "temps_"
	Desc   string
	Perms  []string
	Fields []gfield

	// IDKey is the response key of the server identifier (default "id").
	IDKey string
	// Parents are the attributes leading the import ID ("project_id").
	Parents []string
	// OptionalParents: a parent left unset is left out of the ID (a
	// project-or-platform object).
	OptionalParents bool
	// IDAttr takes the server identifier from an attribute instead of the
	// create response (an association whose create answers 204).
	IDAttr string
	// Singleton resources have no server identifier: their ID is the parents
	// joined, or the resource name when there are none.
	Singleton bool

	Create gop
	Read   gop
	// List reads the collection at Read and picks the element whose IDKey
	// matches; ListKey names the array ("" when the response is the array).
	List    bool
	ListKey string
	Paged   bool
	// Update with no Method makes every argument force replacement.
	Update gop
	// Delete with no Method only forgets the resource.
	Delete     gop
	DeleteNote string

	ReadAfterWrite bool
	// MergeCurrent: the write endpoint replaces the whole document, so send
	// the current one with the configured values overlaid.
	MergeCurrent bool
	BodyEnvelope string

	// Gone reports that a read object means "not there" (a disabled subsystem).
	Gone func(obj map[string]any) bool
	// Normalize reshapes a response object before fields are read from it.
	Normalize func(obj map[string]any)
	// ExpectedVersion names the body key that carries the state's `version`
	// on update, for endpoints with optimistic concurrency.
	ExpectedVersion string
	// BodyHook adjusts a request body before it is sent.
	BodyHook func(body map[string]any, v gvals, create bool)
	// ReadHook replaces the read; it returns nil when the object is gone.
	ReadHook func(ctx context.Context, c *client.Client, v gvals, id string) (map[string]any, error)
	// CreateResult reshapes the create response before the identifier and
	// values are read from it (a response nesting the object next to a
	// one-time secret).
	CreateResult func(obj map[string]any) map[string]any
	// ExpectedCurrent maps body keys to keys of the current object, read
	// just before every write, for endpoints that refuse a write when what
	// the client last saw has changed.
	ExpectedCurrent map[string]string
	// AfterWrite runs follow-up calls after create or update.
	AfterWrite func(ctx context.Context, c *client.Client, plan, prior gvals, id string) error
	// DeleteHook replaces Delete, for objects some of which Temps refuses to
	// delete. A not-found error is ignored.
	DeleteHook func(ctx context.Context, c *client.Client, v gvals, id string) error
}

func (s *gspec) idKey() string {
	if s.IDKey != "" {
		return s.IDKey
	}
	return "id"
}

func (s *gspec) field(name string) *gfield {
	for i := range s.Fields {
		if s.Fields[i].Name == name {
			return &s.Fields[i]
		}
	}
	return nil
}

func (s *gspec) importFormat() string {
	parts := make([]string, 0, len(s.Parents)+1)
	for _, p := range s.Parents {
		if s.OptionalParents {
			p += "?"
		}
		parts = append(parts, "<"+p+">")
	}
	if !s.Singleton {
		k := s.idKey()
		if i := strings.LastIndex(k, "."); i >= 0 {
			k = k[i+1:]
		}
		parts = append(parts, "<"+k+">")
	}
	if len(parts) == 0 {
		return "`" + s.Name + "`"
	}
	return "`" + strings.Join(parts, "/") + "`"
}

func (s *gspec) updatable() bool { return s.Update.Method != "" }

type genericResource struct {
	resourceBase
	s *gspec
}

var (
	_ resource.Resource                = &genericResource{}
	_ resource.ResourceWithImportState = &genericResource{}
)

func genericFactory(s *gspec) func() resource.Resource {
	return func() resource.Resource { return &genericResource{s: s} }
}

func (r *genericResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.s.Name
}

func (r *genericResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	s := r.s
	desc := s.Desc
	if s.DeleteNote != "" {
		desc += "\n\n" + s.DeleteNote
	}
	desc += "\n\nImport with " + s.importFormat() + "."
	if len(s.Perms) > 0 {
		desc += "\n\nRequired API key permissions: " + permList(s.Perms) + "."
	}
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Import ID, " + s.importFormat() + ".",
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
	}
	for i := range s.Fields {
		attrs[s.Fields[i].Name] = r.attribute(&s.Fields[i])
	}
	resp.Schema = schema.Schema{MarkdownDescription: desc, Attributes: attrs}
}

func (r *genericResource) replaces(f *gfield) bool {
	if f.output() || f.Initial {
		return false
	}
	if f.ForceNew || f.CreateOnly || f.Path {
		return true
	}
	for _, p := range r.s.Parents {
		if p == f.Name {
			return true
		}
	}
	return !r.s.updatable()
}

func (r *genericResource) attribute(f *gfield) schema.Attribute {
	required := f.Required
	optional := f.Optional || f.Clearable
	computed := f.output() || (f.Optional && !f.Clearable) || f.Default != nil
	sensitive := f.Sensitive
	desc := f.Desc
	if len(f.OneOf) > 0 {
		desc += " One of `" + joinTicks(f.OneOf) + "`."
	}
	if f.Initial {
		desc += " Only used when the resource is created; changing it later has no effect."
	}
	if f.writeOnly() && !f.Initial && !f.output() {
		desc += " Temps never returns it, so changes made outside Terraform are not detected."
	}
	keep := computed && !f.Volatile
	replace := r.replaces(f)
	switch f.Kind {
	case kInt:
		var pm []planmodifier.Int64
		if keep {
			pm = append(pm, int64planmodifier.UseStateForUnknown())
		}
		if replace {
			if f.writeOnly() {
				pm = append(pm, int64planmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !req.StateValue.IsNull()
				}, importTolerant, importTolerant))
			} else {
				pm = append(pm, int64planmodifier.RequiresReplace())
			}
		}
		a := schema.Int64Attribute{Required: required, Optional: optional, Computed: computed, Sensitive: sensitive,
			MarkdownDescription: desc, PlanModifiers: pm}
		if d, ok := f.Default.(int64); ok {
			a.Default = int64default.StaticInt64(d)
		}
		return a
	case kBool:
		var pm []planmodifier.Bool
		if keep {
			pm = append(pm, boolplanmodifier.UseStateForUnknown())
		}
		if replace {
			if f.writeOnly() {
				pm = append(pm, boolplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.BoolRequest, resp *boolplanmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !req.StateValue.IsNull()
				}, importTolerant, importTolerant))
			} else {
				pm = append(pm, boolplanmodifier.RequiresReplace())
			}
		}
		a := schema.BoolAttribute{Required: required, Optional: optional, Computed: computed, Sensitive: sensitive,
			MarkdownDescription: desc, PlanModifiers: pm}
		if d, ok := f.Default.(bool); ok {
			a.Default = booldefault.StaticBool(d)
		}
		return a
	case kFloat:
		var pm []planmodifier.Float64
		if keep {
			pm = append(pm, float64planmodifier.UseStateForUnknown())
		}
		if replace {
			if f.writeOnly() {
				pm = append(pm, float64planmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.Float64Request, resp *float64planmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !req.StateValue.IsNull()
				}, importTolerant, importTolerant))
			} else {
				pm = append(pm, float64planmodifier.RequiresReplace())
			}
		}
		return schema.Float64Attribute{Required: required, Optional: optional, Computed: computed, Sensitive: sensitive,
			MarkdownDescription: desc, PlanModifiers: pm}
	case kStringList, kIntList:
		var pm []planmodifier.List
		if keep {
			pm = append(pm, listplanmodifier.UseStateForUnknown())
		}
		if replace {
			if f.writeOnly() {
				pm = append(pm, listplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.ListRequest, resp *listplanmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !req.StateValue.IsNull()
				}, importTolerant, importTolerant))
			} else {
				pm = append(pm, listplanmodifier.RequiresReplace())
			}
		}
		return schema.ListAttribute{Required: required, Optional: optional, Computed: computed, Sensitive: sensitive,
			ElementType: elemType(f.Kind), MarkdownDescription: desc, PlanModifiers: pm}
	case kStringSet, kIntSet:
		var pm []planmodifier.Set
		if keep {
			pm = append(pm, setplanmodifier.UseStateForUnknown())
		}
		if replace {
			if f.writeOnly() {
				pm = append(pm, setplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.SetRequest, resp *setplanmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !req.StateValue.IsNull()
				}, importTolerant, importTolerant))
			} else {
				pm = append(pm, setplanmodifier.RequiresReplace())
			}
		}
		return schema.SetAttribute{Required: required, Optional: optional, Computed: computed, Sensitive: sensitive,
			ElementType: elemType(f.Kind), MarkdownDescription: desc, PlanModifiers: pm}
	case kStringMap:
		var pm []planmodifier.Map
		if keep {
			pm = append(pm, mapplanmodifier.UseStateForUnknown())
		}
		if replace {
			if f.writeOnly() {
				pm = append(pm, mapplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.MapRequest, resp *mapplanmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !req.StateValue.IsNull()
				}, importTolerant, importTolerant))
			} else {
				pm = append(pm, mapplanmodifier.RequiresReplace())
			}
		}
		return schema.MapAttribute{Required: required, Optional: optional, Computed: computed, Sensitive: sensitive,
			ElementType: types.StringType, MarkdownDescription: desc, PlanModifiers: pm}
	default:
		var pm []planmodifier.String
		if keep {
			pm = append(pm, stringplanmodifier.UseStateForUnknown())
		}
		if replace {
			if f.writeOnly() {
				pm = append(pm, stringplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !req.StateValue.IsNull()
				}, importTolerant, importTolerant))
			} else {
				pm = append(pm, stringplanmodifier.RequiresReplace())
			}
		} else if f.ReplaceIf != nil {
			pm = append(pm, stringplanmodifier.RequiresReplaceIf(f.ReplaceIf, f.ReplaceIfDesc, f.ReplaceIfDesc))
		}
		var vs []validator.String
		if len(f.OneOf) > 0 && !f.output() {
			vs = append(vs, stringvalidator.OneOf(f.OneOf...))
		}
		if f.Kind == kJSON && !f.output() {
			vs = append(vs, jsonString{})
			desc += " A JSON document; use `jsonencode(...)`."
		}
		a := schema.StringAttribute{Required: required, Optional: optional, Computed: computed, Sensitive: sensitive,
			MarkdownDescription: desc, PlanModifiers: pm, Validators: vs}
		if d, ok := f.Default.(string); ok {
			a.Default = stringdefault.StaticString(d)
		}
		return a
	}
}

// importTolerant explains why a value Temps never returns only forces
// replacement once state holds one: an import leaves it empty.
const importTolerant = "Changing it replaces the resource, except when state has no value for it (after an import)."

func elemType(k fieldKind) attr.Type {
	switch k {
	case kIntList, kIntSet:
		return types.Int64Type
	default:
		return types.StringType
	}
}

func nullOf(k fieldKind) attr.Value {
	switch k {
	case kInt:
		return types.Int64Null()
	case kBool:
		return types.BoolNull()
	case kFloat:
		return types.Float64Null()
	case kStringList, kIntList:
		return types.ListNull(elemType(k))
	case kStringSet, kIntSet:
		return types.SetNull(elemType(k))
	case kStringMap:
		return types.MapNull(types.StringType)
	default:
		return types.StringNull()
	}
}

// load reads every attribute from a plan or state.
func (r *genericResource) load(ctx context.Context, get func(context.Context, path.Path, any) diag.Diagnostics, diags *diag.Diagnostics) gvals {
	v := gvals{}
	var id types.String
	diags.Append(get(ctx, path.Root("id"), &id)...)
	v["id"] = id
	for i := range r.s.Fields {
		f := &r.s.Fields[i]
		p := path.Root(f.Name)
		switch f.Kind {
		case kInt:
			var x types.Int64
			diags.Append(get(ctx, p, &x)...)
			v[f.Name] = x
		case kBool:
			var x types.Bool
			diags.Append(get(ctx, p, &x)...)
			v[f.Name] = x
		case kFloat:
			var x types.Float64
			diags.Append(get(ctx, p, &x)...)
			v[f.Name] = x
		case kStringList, kIntList:
			var x types.List
			diags.Append(get(ctx, p, &x)...)
			v[f.Name] = x
		case kStringSet, kIntSet:
			var x types.Set
			diags.Append(get(ctx, p, &x)...)
			v[f.Name] = x
		case kStringMap:
			var x types.Map
			diags.Append(get(ctx, p, &x)...)
			v[f.Name] = x
		default:
			var x types.String
			diags.Append(get(ctx, p, &x)...)
			v[f.Name] = x
		}
	}
	return v
}

func (r *genericResource) save(ctx context.Context, st *tfsdk.State, v gvals, diags *diag.Diagnostics) {
	diags.Append(st.SetAttribute(ctx, path.Root("id"), v["id"])...)
	for i := range r.s.Fields {
		f := &r.s.Fields[i]
		val := v[f.Name]
		if val == nil || val.IsUnknown() {
			val = nullOf(f.Kind)
		}
		diags.Append(st.SetAttribute(ctx, path.Root(f.Name), val)...)
	}
}

// --- identifiers and paths ---

func (r *genericResource) tfID(v gvals, serverID string) string {
	parts := make([]string, 0, len(r.s.Parents)+1)
	for _, p := range r.s.Parents {
		if r.s.OptionalParents && !hasValue(v[p]) {
			continue
		}
		parts = append(parts, scalarString(v[p]))
	}
	if !r.s.Singleton {
		parts = append(parts, serverID)
	}
	if len(parts) == 0 {
		return r.s.Name
	}
	return strings.Join(parts, "/")
}

func (r *genericResource) serverID(v gvals) string {
	if r.s.Singleton {
		return ""
	}
	id, ok := v["id"].(types.String)
	if !ok || !known(id) {
		return ""
	}
	s := id.ValueString()
	n := 0
	for _, p := range r.s.Parents {
		if !r.s.OptionalParents || hasValue(v[p]) {
			n++
		}
	}
	if n > 0 {
		parts := strings.SplitN(s, "/", n+1)
		return parts[len(parts)-1]
	}
	return s
}

// hasValue reports whether an optional parent has a value. At create an optional
// parent left out of the configuration is still unknown.
func hasValue(v attr.Value) bool { return v != nil && !v.IsNull() && !v.IsUnknown() }

func scalarString(v attr.Value) string {
	switch x := v.(type) {
	case types.String:
		return x.ValueString()
	case types.Int64:
		return strconv.FormatInt(x.ValueInt64(), 10)
	case types.Bool:
		return strconv.FormatBool(x.ValueBool())
	case types.Float64:
		return strconv.FormatFloat(x.ValueFloat64(), 'f', -1, 64)
	}
	return ""
}

func (r *genericResource) render(o gop, v gvals, id string) string {
	if o.PathFn != nil {
		return o.PathFn(v, id)
	}
	return renderPath(o.Path, v, id)
}

func renderPath(tmpl string, v gvals, id string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(tmpl, '{')
		if i < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		j := strings.IndexByte(tmpl[i:], '}')
		if j < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		b.WriteString(tmpl[:i])
		name := tmpl[i+1 : i+j]
		if name == "id" {
			b.WriteString(client.PathSegment(id))
		} else {
			b.WriteString(client.PathSegment(scalarString(v[name])))
		}
		tmpl = tmpl[i+j+1:]
	}
}

// --- JSON documents ---

func getPath(obj map[string]any, key string) (any, bool) {
	if key == "." {
		return obj, obj != nil
	}
	var cur any = obj
	for _, part := range strings.Split(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func setPath(obj map[string]any, key string, val any) {
	parts := strings.Split(key, ".")
	for _, part := range parts[:len(parts)-1] {
		next, ok := obj[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			obj[part] = next
		}
		obj = next
	}
	obj[parts[len(parts)-1]] = val
}

// overlay deep-merges src into dst (objects merge, everything else replaces).
func overlay(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for k, sv := range src {
		if sm, ok := sv.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				dst[k] = overlay(dm, sm)
				continue
			}
		}
		dst[k] = sv
	}
	return dst
}

func asObject(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func unwrap(v any, envelope string) map[string]any {
	obj := asObject(v)
	if envelope == "" || obj == nil {
		return obj
	}
	inner, _ := getPath(obj, envelope)
	return asObject(inner)
}

func decodeJSON(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func toJSON(ctx context.Context, f *gfield, v attr.Value, diags *diag.Diagnostics) any {
	switch x := v.(type) {
	case types.String:
		if f.Kind == kJSON {
			doc, err := decodeJSON(x.ValueString())
			if err != nil {
				diags.AddAttributeError(path.Root(f.Name), "Invalid JSON", err.Error())
				return nil
			}
			return doc
		}
		return x.ValueString()
	case types.Int64:
		return x.ValueInt64()
	case types.Bool:
		return x.ValueBool()
	case types.Float64:
		return x.ValueFloat64()
	case types.List:
		if f.Kind == kIntList {
			var out []int64
			diags.Append(x.ElementsAs(ctx, &out, false)...)
			return nonNil(out)
		}
		var out []string
		diags.Append(x.ElementsAs(ctx, &out, false)...)
		return nonNil(out)
	case types.Set:
		if f.Kind == kIntSet {
			var out []int64
			diags.Append(x.ElementsAs(ctx, &out, false)...)
			return nonNil(out)
		}
		var out []string
		diags.Append(x.ElementsAs(ctx, &out, false)...)
		return nonNil(out)
	case types.Map:
		out := map[string]string{}
		diags.Append(x.ElementsAs(ctx, &out, false)...)
		return out
	}
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func numberInt(raw any) (int64, bool) {
	switch n := raw.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
		if f, err := n.Float64(); err == nil {
			return int64(f), true
		}
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case string:
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
}

func rawString(raw any) string {
	switch x := raw.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// fromJSON converts a response value, keeping the prior spelling where the
// server's value means the same thing.
func fromJSON(f *gfield, raw any, prior attr.Value) (attr.Value, error) {
	if raw == nil {
		return nullOf(f.Kind), nil
	}
	bad := func() (attr.Value, error) {
		return nullOf(f.Kind), fmt.Errorf("attribute %s: the server returned %s where a %s was expected", f.Name, rawString(raw), kindName(f.Kind))
	}
	switch f.Kind {
	case kInt:
		i, ok := numberInt(raw)
		if !ok {
			return bad()
		}
		return types.Int64Value(i), nil
	case kBool:
		b, ok := raw.(bool)
		if !ok {
			return bad()
		}
		return types.BoolValue(b), nil
	case kFloat:
		n, ok := raw.(json.Number)
		if !ok {
			return bad()
		}
		fl, err := n.Float64()
		if err != nil {
			return bad()
		}
		return types.Float64Value(fl), nil
	case kStringList, kStringSet, kIntList, kIntSet:
		arr, ok := raw.([]any)
		if !ok {
			return bad()
		}
		elems := make([]attr.Value, 0, len(arr))
		for _, e := range arr {
			if f.Kind == kIntList || f.Kind == kIntSet {
				i, ok := numberInt(e)
				if !ok {
					return bad()
				}
				elems = append(elems, types.Int64Value(i))
			} else {
				elems = append(elems, types.StringValue(rawString(e)))
			}
		}
		if f.Kind == kStringList || f.Kind == kIntList {
			l, d := types.ListValue(elemType(f.Kind), elems)
			if d.HasError() {
				return bad()
			}
			return l, nil
		}
		s, d := types.SetValue(elemType(f.Kind), elems)
		if d.HasError() {
			return bad()
		}
		return s, nil
	case kStringMap:
		m, ok := raw.(map[string]any)
		if !ok {
			return bad()
		}
		elems := map[string]attr.Value{}
		for k, e := range m {
			elems[k] = types.StringValue(rawString(e))
		}
		mv, d := types.MapValue(types.StringType, elems)
		if d.HasError() {
			return bad()
		}
		return mv, nil
	case kJSON:
		b, err := json.Marshal(raw)
		if err != nil {
			return bad()
		}
		p, _ := prior.(types.String)
		if known(p) && f.Subset && jsonSubset(p.ValueString(), string(b)) {
			return p, nil
		}
		return jsonValue(p, b), nil
	default:
		s := rawString(raw)
		if p, ok := prior.(types.String); ok && known(p) && p.ValueString() != s {
			if same := timeValue(p, &s); same.Equal(p) {
				return p, nil
			}
		}
		return types.StringValue(s), nil
	}
}

func kindName(k fieldKind) string {
	switch k {
	case kInt:
		return "number"
	case kBool:
		return "boolean"
	case kFloat:
		return "number"
	case kStringList, kStringSet, kIntList, kIntSet:
		return "list"
	case kStringMap:
		return "object"
	case kJSON:
		return "JSON value"
	}
	return "string"
}

// jsonSubset reports whether every key of the configured document has the
// same value in the server's (which may hold more keys).
func jsonSubset(configured, server string) bool {
	var a, b any
	if json.Unmarshal([]byte(configured), &a) != nil || json.Unmarshal([]byte(server), &b) != nil {
		return false
	}
	return subsetOf(a, b)
}

func subsetOf(a, b any) bool {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		for k, av := range am {
			bv, ok := bm[k]
			if !ok {
				if av == nil {
					continue
				}
				return false
			}
			if !subsetOf(av, bv) {
				return false
			}
		}
		return true
	}
	al, aok := a.([]any)
	bl, bok := b.([]any)
	if aok && bok {
		if len(al) != len(bl) {
			return false
		}
		for i := range al {
			if !subsetOf(al[i], bl[i]) {
				return false
			}
		}
		return true
	}
	if as, ok := a.(string); ok {
		if bs, ok := b.(string); ok && as != bs && masked(bs) {
			// Temps masks stored secrets on read; the configured value
			// cannot be compared, so it is taken as unchanged.
			return true
		}
	}
	return reflect.DeepEqual(a, b)
}

func masked(s string) bool {
	return s != "" && strings.Trim(s, "*•") == "" || strings.HasSuffix(s, "***") || strings.HasPrefix(s, "***")
}

// --- request bodies and responses ---

func (r *genericResource) body(ctx context.Context, v gvals, create bool, diags *diag.Diagnostics) map[string]any {
	body := map[string]any{}
	for i := range r.s.Fields {
		f := &r.s.Fields[i]
		if f.Path || f.output() {
			continue
		}
		if !create && (f.CreateOnly || f.Initial) {
			continue
		}
		val := v[f.Name]
		if val == nil || val.IsUnknown() {
			continue
		}
		if val.IsNull() {
			if !create && f.Clearable && f.JSON != "." {
				setPath(body, f.key(), f.ClearWith)
			}
			continue
		}
		j := toJSON(ctx, f, val, diags)
		if f.JSON == "." {
			doc, ok := j.(map[string]any)
			if !ok {
				diags.AddAttributeError(path.Root(f.Name), "Expected a JSON object", f.Name+" must be a JSON object.")
				continue
			}
			body = overlay(body, doc)
			continue
		}
		setPath(body, f.key(), j)
	}
	if r.s.BodyHook != nil {
		r.s.BodyHook(body, v, create)
	}
	return body
}

// apply copies a response object into v. Values the server does not return
// keep what v holds (configuration on write, state on read).
func (r *genericResource) apply(v gvals, obj map[string]any, diags *diag.Diagnostics) {
	if r.s.Normalize != nil && obj != nil {
		r.s.Normalize(obj)
	}
	for i := range r.s.Fields {
		f := &r.s.Fields[i]
		if f.writeOnly() || f.Initial {
			continue
		}
		// Path values come from configuration; read them only to fill an
		// import.
		if f.Path && v[f.Name] != nil && !v[f.Name].IsNull() && !v[f.Name].IsUnknown() {
			continue
		}
		raw, ok := getPath(obj, f.readKey())
		if !ok {
			continue
		}
		// A field cleared by sending "" may come back as "": it is unset.
		if s, isStr := raw.(string); isStr && s == "" && f.ClearWith == "" {
			raw = nil
		}
		nv, err := fromJSON(f, raw, v[f.Name])
		if err != nil {
			diags.AddError("Unexpected response from Temps", err.Error())
			continue
		}
		if f.Time && sameInstant(v[f.Name], nv) {
			continue
		}
		v[f.Name] = nv
	}
	for i := range r.s.Fields {
		f := &r.s.Fields[i]
		if val := v[f.Name]; val == nil || val.IsUnknown() {
			v[f.Name] = nullOf(f.Kind)
		}
	}
}

func (r *genericResource) what(v gvals) string {
	id := scalarString(v["id"])
	if id == "" {
		return "temps_" + r.s.Name
	}
	return fmt.Sprintf("temps_%s %q", r.s.Name, id)
}

// readObject fetches the object; nil means it no longer exists.
func (r *genericResource) readObject(ctx context.Context, v gvals, id string) (map[string]any, error) {
	s := r.s
	if s.ReadHook != nil {
		return s.ReadHook(ctx, r.client, v, id)
	}
	p := r.render(s.Read, v, id)
	if !s.List {
		raw, err := r.client.Raw(ctx, "GET", p, s.Read.query(v), nil)
		if client.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		obj := unwrap(raw, s.Read.Envelope)
		if obj != nil && s.Gone != nil && s.Gone(obj) {
			return nil, nil
		}
		return obj, nil
	}
	const pageSize = 100
	for page := 1; page <= 1000; page++ {
		q := s.Read.query(v)
		if s.Paged {
			if q == nil {
				q = url.Values{}
			}
			q.Set("page", strconv.Itoa(page))
			q.Set("page_size", strconv.Itoa(pageSize))
		}
		raw, err := r.client.Raw(ctx, "GET", p, q, nil)
		if client.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var items []any
		if s.ListKey == "" {
			items, _ = raw.([]any)
		} else if obj := unwrap(raw, s.Read.Envelope); obj != nil {
			got, _ := getPath(obj, s.ListKey)
			items, _ = got.([]any)
		}
		for _, it := range items {
			obj := asObject(it)
			if k, ok := getPath(obj, s.idKey()); ok && rawString(k) == id {
				if s.Gone != nil && s.Gone(obj) {
					return nil, nil
				}
				return obj, nil
			}
		}
		if !s.Paged || len(items) < pageSize {
			return nil, nil
		}
	}
	return nil, nil
}

func (r *genericResource) write(ctx context.Context, o gop, v, prior gvals, id string, create bool, diags *diag.Diagnostics) (map[string]any, bool) {
	body := r.body(ctx, v, create, diags)
	if diags.HasError() {
		return nil, false
	}
	if len(r.s.ExpectedCurrent) > 0 {
		current, err := r.readObject(ctx, v, id)
		if err != nil {
			addAPIError(diags, "Unable to read the current state of "+r.what(v), err, r.s.Perms)
			return nil, false
		}
		for _, k := range sortedKeys(r.s.ExpectedCurrent) {
			if cur, ok := getPath(current, r.s.ExpectedCurrent[k]); ok {
				setPath(body, k, cur)
			}
		}
	}
	if !create && r.s.ExpectedVersion != "" {
		// A version that changed since the refresh makes Temps refuse the
		// write instead of overwriting someone else's change.
		body[r.s.ExpectedVersion] = scalarJSON(prior["version"])
	}
	if !create && len(body) == 0 && !r.s.MergeCurrent {
		// Nothing the update endpoint takes has changed (only values the
		// server keeps, such as an imported resource's secrets).
		obj, err := r.readObject(ctx, v, id)
		if err != nil {
			addAPIError(diags, "Unable to read "+r.what(v), err, r.s.Perms)
			return nil, false
		}
		return obj, true
	}
	if r.s.MergeCurrent {
		current, err := r.readObject(ctx, v, id)
		if err != nil {
			addAPIError(diags, "Unable to read the current settings of "+r.what(v), err, r.s.Perms)
			return nil, false
		}
		body = overlay(current, body)
	}
	var in any = body
	if r.s.BodyEnvelope != "" {
		in = map[string]any{r.s.BodyEnvelope: body}
	}
	raw, err := r.client.Raw(ctx, o.Method, r.render(o, v, id), o.query(v), in)
	if err != nil {
		verb := "update"
		if create {
			verb = "create"
		}
		addAPIError(diags, "Unable to "+verb+" "+r.what(v), err, r.s.Perms)
		return nil, false
	}
	env := o.Envelope
	if env == "" {
		env = r.s.Read.Envelope
		if r.s.List {
			env = ""
		}
	}
	return unwrap(raw, env), true
}

func (r *genericResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	v := r.load(ctx, req.Plan.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	plan := copyVals(v)
	obj, ok := r.write(ctx, r.s.Create, v, nil, "", true, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.s.CreateResult != nil {
		obj = r.s.CreateResult(obj)
	}
	id := ""
	if r.s.IDAttr != "" {
		id = scalarString(v[r.s.IDAttr])
	} else if !r.s.Singleton {
		raw, found := getPath(obj, r.s.idKey())
		if !found || raw == nil {
			resp.Diagnostics.AddError("Unexpected response from Temps",
				fmt.Sprintf("Creating temps_%s returned no %q identifier; the object may exist without being tracked. Response: %v", r.s.Name, r.s.idKey(), obj))
			return
		}
		id = rawString(raw)
	}
	v["id"] = types.StringValue(r.tfID(v, id))
	// From here on the object exists: save what is known even if a later
	// step fails, so it is not orphaned.
	if r.s.AfterWrite != nil {
		if err := r.s.AfterWrite(ctx, r.client, plan, nil, id); err != nil {
			addAPIError(&resp.Diagnostics, "Unable to finish creating "+r.what(v), err, r.s.Perms)
		}
	}
	if r.s.ReadAfterWrite || obj == nil || r.s.AfterWrite != nil {
		fresh, err := r.readObject(ctx, v, id)
		if err != nil {
			addAPIError(&resp.Diagnostics, "Unable to read back "+r.what(v), err, r.s.Perms)
		} else if fresh != nil {
			// Keep what only the create response carries (a secret shown
			// once) under the values read back.
			obj = overlay(obj, fresh)
		}
	}
	r.apply(v, obj, &resp.Diagnostics)
	r.save(ctx, &resp.State, v, &resp.Diagnostics)
}

func (r *genericResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	v := r.load(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	obj, err := r.readObject(ctx, v, r.serverID(v))
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read "+r.what(v), err, r.s.Perms)
		return
	}
	if obj == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	r.apply(v, obj, &resp.Diagnostics)
	r.save(ctx, &resp.State, v, &resp.Diagnostics)
}

func (r *genericResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	v := r.load(ctx, req.Plan.GetAttribute, &resp.Diagnostics)
	prior := r.load(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	plan := copyVals(v)
	v["id"] = prior["id"]
	id := r.serverID(prior)
	var obj map[string]any
	if r.s.updatable() {
		var ok bool
		if obj, ok = r.write(ctx, r.s.Update, v, prior, id, false, &resp.Diagnostics); !ok {
			return
		}
	}
	// Without an update endpoint every argument forces replacement, except
	// values Temps never returns arriving after an import: they only need
	// recording, which the read below does.
	if r.s.AfterWrite != nil {
		if err := r.s.AfterWrite(ctx, r.client, plan, prior, id); err != nil {
			addAPIError(&resp.Diagnostics, "Unable to finish updating "+r.what(v), err, r.s.Perms)
		}
	}
	if r.s.ReadAfterWrite || obj == nil || r.s.AfterWrite != nil {
		fresh, err := r.readObject(ctx, v, id)
		if err != nil {
			addAPIError(&resp.Diagnostics, "Unable to read back "+r.what(v), err, r.s.Perms)
			return
		}
		if fresh == nil {
			resp.Diagnostics.AddError("Object disappeared", r.what(v)+" was updated but can no longer be read back.")
			return
		}
		obj = fresh
	}
	r.apply(v, obj, &resp.Diagnostics)
	r.save(ctx, &resp.State, v, &resp.Diagnostics)
}

func (r *genericResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	v := r.load(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.s.DeleteHook != nil {
		if err := r.s.DeleteHook(ctx, r.client, v, r.serverID(v)); err != nil && !client.IsNotFound(err) {
			addAPIError(&resp.Diagnostics, "Unable to delete "+r.what(v), err, r.s.Perms)
		}
		return
	}
	o := r.s.Delete
	if o.Method == "" {
		return
	}
	var in any
	if o.Body != nil {
		in = o.Body
	}
	_, err := r.client.Raw(ctx, o.Method, r.render(o, v, r.serverID(v)), o.query(v), in)
	if err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Unable to delete "+r.what(v), err, r.s.Perms)
	}
}

func (r *genericResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	s := r.s
	want := len(s.Parents)
	if !s.Singleton {
		want++
	}
	if want == 0 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), s.Name)...)
		return
	}
	parts := strings.Split(req.ID, "/")
	if !s.Singleton && len(parts) > want {
		// The last segment is the server identifier and may contain "/".
		parts = append(parts[:want-1], strings.Join(parts[want-1:], "/"))
	}
	parents := s.Parents
	if s.OptionalParents && len(parts) < want && len(parts) >= want-len(s.Parents) {
		// Leading parents left out: the object is not scoped by them.
		parents = s.Parents[len(s.Parents)-(len(parts)-(want-len(s.Parents))):]
		want = len(parts)
	}
	if len(parts) != want || slicesContainEmpty(parts) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("import ID %q must have the form %s", req.ID, s.importFormat()))
		return
	}
	for i, p := range parents {
		f := s.field(p)
		if f == nil {
			resp.Diagnostics.AddError("Provider bug", "unknown parent attribute "+p)
			return
		}
		if f.Kind == kInt {
			n, err := parseID(parts[i])
			if err != nil && f.ZeroOK && strings.TrimSpace(parts[i]) == "0" {
				n, err = 0, nil
			}
			if err != nil {
				resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("import ID %q must have the form %s: %s", req.ID, s.importFormat(), err))
				return
			}
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(p), n)...)
		} else {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(p), parts[i])...)
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.Join(parts, "/"))...)
}

func slicesContainEmpty(parts []string) bool {
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return true
		}
	}
	return false
}

func copyVals(v gvals) gvals {
	out := make(gvals, len(v))
	for k, x := range v {
		out[k] = x
	}
	return out
}

// sortedKeys is used by specs that build documentation from maps.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sameInstant reports whether two RFC 3339 strings name the same time.
func sameInstant(a, b attr.Value) bool {
	as, aok := a.(types.String)
	bs, bok := b.(types.String)
	if !aok || !bok || !known(as) || !known(bs) {
		return false
	}
	at, err1 := time.Parse(time.RFC3339Nano, as.ValueString())
	bt, err2 := time.Parse(time.RFC3339Nano, bs.ValueString())
	return err1 == nil && err2 == nil && at.Equal(bt)
}
