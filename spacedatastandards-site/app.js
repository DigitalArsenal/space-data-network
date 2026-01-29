/**
 * Space Data Standards - Site Application
 * Schema explorer, field viewer, download center, and API simulation
 */

// ============================================================================
// State
// ============================================================================
let currentCategory = 'all';
let currentView = 'grid';
let currentSchema = null;
let currentFormat = 'json-schema';

// ============================================================================
// Navigation
// ============================================================================
function toggleMobileMenu() {
  document.getElementById('mobileMenu').classList.toggle('open');
}

function closeMobileMenu() {
  document.getElementById('mobileMenu').classList.remove('open');
}

// ============================================================================
// Schema Grid
// ============================================================================
function renderSchemaGrid() {
  const grid = document.getElementById('schemaGrid');
  const searchTerm = document.getElementById('schemaSearch').value.toLowerCase();

  const filtered = SCHEMAS.filter(s => {
    const matchesCategory = currentCategory === 'all' || s.category === currentCategory;
    const matchesSearch = !searchTerm ||
      s.name.toLowerCase().includes(searchTerm) ||
      s.fullName.toLowerCase().includes(searchTerm) ||
      s.description.toLowerCase().includes(searchTerm);
    return matchesCategory && matchesSearch;
  });

  grid.className = currentView === 'list' ? 'schema-grid list-view' : 'schema-grid';

  grid.innerHTML = filtered.map(s => `
    <div class="schema-card" onclick="openSchemaModal('${s.name}')">
      <div class="schema-card-header">
        <span class="schema-name">${s.name}</span>
        <span class="schema-version">${s.version}</span>
      </div>
      <div class="schema-full-name">${s.fullName}</div>
      <div class="schema-card-meta">
        <span class="schema-badge ${s.category}">${SCHEMA_CATEGORIES[s.category]?.label || s.category}</span>
        <span class="schema-field-count">${s.fields.length} fields</span>
      </div>
    </div>
  `).join('');

  document.getElementById('schemaCount').textContent = SCHEMAS.length;
}

function filterSchemas() {
  renderSchemaGrid();
}

function setCategory(cat) {
  currentCategory = cat;
  document.querySelectorAll('.filter-btn').forEach(b => {
    b.classList.toggle('active', b.dataset.category === cat);
  });
  renderSchemaGrid();
}

function setView(view) {
  currentView = view;
  document.querySelectorAll('.view-btn').forEach(b => {
    b.classList.toggle('active', b.dataset.view === view);
  });
  renderSchemaGrid();
}

// ============================================================================
// Schema Modal
// ============================================================================
function openSchemaModal(name) {
  currentSchema = SCHEMAS.find(s => s.name === name);
  if (!currentSchema) return;

  document.getElementById('modalTitle').textContent = currentSchema.name;
  document.getElementById('modalSubtitle').textContent = currentSchema.fullName;
  document.getElementById('modalCategory').textContent = SCHEMA_CATEGORIES[currentSchema.category]?.label || currentSchema.category;
  document.getElementById('modalCategory').className = `schema-badge ${currentSchema.category}`;
  document.getElementById('modalVersion').textContent = `v${currentSchema.version}`;
  document.getElementById('modalDescription').textContent = currentSchema.description;

  currentFormat = 'json-schema';
  updateFormatTabs();
  renderFieldExplorer();

  document.getElementById('schemaModal').classList.add('open');
  document.body.style.overflow = 'hidden';
}

function closeSchemaModal() {
  document.getElementById('schemaModal').classList.remove('open');
  document.body.style.overflow = '';
  currentSchema = null;
}

function setFormat(format) {
  currentFormat = format;
  updateFormatTabs();

  if (format === 'json-schema') {
    document.getElementById('fieldExplorer').style.display = '';
    document.getElementById('schemaCodeView').style.display = 'none';
    renderFieldExplorer();
  } else {
    document.getElementById('fieldExplorer').style.display = 'none';
    document.getElementById('schemaCodeView').style.display = '';
    renderCodeView(format);
  }
}

function updateFormatTabs() {
  document.querySelectorAll('.schema-tab').forEach(t => {
    t.classList.toggle('active', t.dataset.format === currentFormat);
  });
}

// ============================================================================
// Field Explorer
// ============================================================================
function renderFieldExplorer() {
  if (!currentSchema) return;
  const list = document.getElementById('fieldList');

  list.innerHTML = currentSchema.fields.map((f, i) => `
    <div class="field-item" id="field-${i}">
      <div class="field-header" onclick="toggleField(${i})">
        <svg class="field-toggle" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m9 18 6-6-6-6"/></svg>
        <span class="field-name">${f.name}</span>
        <span class="field-type">${f.type}</span>
        ${f.required ? '<span class="field-required">required</span>' : ''}
        <button class="field-path-btn" onclick="event.stopPropagation(); copyFieldPath('${currentSchema.name}.${f.name}')" title="Copy field path">${currentSchema.name}.${f.name}</button>
      </div>
      <div class="field-details">
        <div class="field-detail-row">
          <span class="field-detail-label">Description</span>
          <span class="field-detail-value">${f.desc}</span>
        </div>
        <div class="field-detail-row">
          <span class="field-detail-label">x-flatbuffer-type</span>
          <span class="field-detail-value">${f.fbType}</span>
        </div>
        <div class="field-detail-row">
          <span class="field-detail-label">x-flatbuffer-field-id</span>
          <span class="field-detail-value">${f.fbId}</span>
        </div>
        ${f.default ? `<div class="field-detail-row"><span class="field-detail-label">x-flatbuffer-default</span><span class="field-detail-value">${f.default}</span></div>` : ''}
        ${f.enum ? `<div class="field-detail-row"><span class="field-detail-label">x-flatbuffer-enum</span><span class="field-detail-value">${f.enum}</span></div>` : ''}
        ${f.required ? `<div class="field-detail-row"><span class="field-detail-label">x-flatbuffer-required</span><span class="field-detail-value">true</span></div>` : ''}
      </div>
    </div>
  `).join('');
}

function toggleField(idx) {
  document.getElementById(`field-${idx}`).classList.toggle('expanded');
}

function expandAllFields() {
  document.querySelectorAll('.field-item').forEach(el => el.classList.add('expanded'));
}

function collapseAllFields() {
  document.querySelectorAll('.field-item').forEach(el => el.classList.remove('expanded'));
}

function copyFieldPath(path) {
  navigator.clipboard.writeText(path).then(() => showToast('Copied: ' + path));
}

// ============================================================================
// Code View (generate code for different formats)
// ============================================================================
function renderCodeView(format) {
  if (!currentSchema) return;

  const labelMap = {
    'flatbuffers': 'FlatBuffers (.fbs)',
    'typescript': 'TypeScript',
    'go': 'Go',
    'python': 'Python',
    'rust': 'Rust',
  };

  document.getElementById('codeFormatLabel').textContent = labelMap[format] || format;
  document.getElementById('schemaCode').textContent = generateCode(currentSchema, format);
}

function generateCode(schema, format) {
  switch (format) {
    case 'flatbuffers': return generateFbs(schema);
    case 'typescript': return generateTS(schema);
    case 'go': return generateGo(schema);
    case 'python': return generatePython(schema);
    case 'rust': return generateRust(schema);
    default: return '';
  }
}

function generateFbs(s) {
  const lines = [`// ${s.fullName}`, `// Version: ${s.version}`, ''];
  if (s.includes?.length) {
    s.includes.forEach(inc => lines.push(`include "../${inc}/main.fbs";`));
    lines.push('');
  }
  lines.push(`/// ${s.description.split('.')[0]}`);
  lines.push(`table ${s.name} {`);
  s.fields.forEach(f => {
    lines.push(`  /// ${f.desc}`);
    const req = f.required ? ' (required)' : '';
    const def = f.default ? ` = ${f.default}` : '';
    lines.push(`  ${f.name}:${f.fbType}${def}${req};`);
  });
  lines.push('}');
  if (s.fileIdentifier) {
    lines.push('');
    lines.push(`root_type ${s.name};`);
    lines.push(`file_identifier "${s.fileIdentifier}";`);
  }
  return lines.join('\n');
}

function generateTS(s) {
  const typeMap = { string: 'string', number: 'number', integer: 'number', boolean: 'boolean', array: 'any[]', object: 'Record<string, any>' };
  const lines = [`/** ${s.fullName} - v${s.version} */`, `export interface ${s.name} {`];
  s.fields.forEach(f => {
    const opt = f.required ? '' : '?';
    lines.push(`  /** ${f.desc} */`);
    lines.push(`  ${f.name}${opt}: ${typeMap[f.type] || 'any'};`);
  });
  lines.push('}');
  return lines.join('\n');
}

function generateGo(s) {
  const typeMap = { string: 'string', number: 'float64', integer: 'uint32', boolean: 'bool', array: '[]interface{}', object: 'map[string]interface{}' };
  const lines = [`// ${s.fullName} - v${s.version}`, `package sds`, '', `type ${s.name} struct {`];
  s.fields.forEach(f => {
    const tag = `\`json:"${f.name},omitempty" flatbuffer:"${f.fbId}"\``;
    lines.push(`\t// ${f.desc}`);
    lines.push(`\t${f.name} ${typeMap[f.type] || 'interface{}'} ${tag}`);
  });
  lines.push('}');
  return lines.join('\n');
}

function generatePython(s) {
  const typeMap = { string: 'str', number: 'float', integer: 'int', boolean: 'bool', array: 'list', object: 'dict' };
  const lines = [`"""${s.fullName} - v${s.version}"""`, `from dataclasses import dataclass, field`, `from typing import Optional, List`, '', '', `@dataclass`, `class ${s.name}:`];
  lines.push(`    """${s.description.split('.')[0]}"""`);
  s.fields.forEach(f => {
    const t = typeMap[f.type] || 'object';
    if (f.required) {
      lines.push(`    ${f.name}: ${t}  # ${f.desc}`);
    } else {
      lines.push(`    ${f.name}: Optional[${t}] = None  # ${f.desc}`);
    }
  });
  return lines.join('\n');
}

function generateRust(s) {
  const typeMap = { string: 'String', number: 'f64', integer: 'u32', boolean: 'bool', array: 'Vec<serde_json::Value>', object: 'std::collections::HashMap<String, serde_json::Value>' };
  const lines = [`/// ${s.fullName} - v${s.version}`, `#[derive(Debug, Clone, Serialize, Deserialize)]`, `pub struct ${s.name} {`];
  s.fields.forEach(f => {
    const t = typeMap[f.type] || 'serde_json::Value';
    lines.push(`    /// ${f.desc}`);
    if (f.required) {
      lines.push(`    pub ${f.name.toLowerCase()}: ${t},`);
    } else {
      lines.push(`    #[serde(skip_serializing_if = "Option::is_none")]`);
      lines.push(`    pub ${f.name.toLowerCase()}: Option<${t}>,`);
    }
  });
  lines.push('}');
  return lines.join('\n');
}

function copySchemaCode() {
  const code = document.getElementById('schemaCode').textContent;
  navigator.clipboard.writeText(code).then(() => showToast('Code copied to clipboard'));
}

// ============================================================================
// JSON Schema Generation
// ============================================================================
function generateJsonSchema(schema) {
  const properties = {};
  const required = [];

  schema.fields.forEach(f => {
    const prop = {
      description: f.desc,
      'x-flatbuffer-type': f.fbType,
      'x-flatbuffer-field-id': f.fbId,
    };

    switch (f.type) {
      case 'string': prop.type = 'string'; break;
      case 'number': prop.type = 'number'; break;
      case 'integer': prop.type = 'integer'; break;
      case 'boolean': prop.type = 'boolean'; break;
      case 'array': prop.type = 'array'; prop.items = {}; break;
      case 'object': prop.type = 'object'; break;
    }

    if (f.default) prop['x-flatbuffer-default'] = f.default;
    if (f.enum) prop['x-flatbuffer-enum'] = f.enum;
    if (f.required) {
      prop['x-flatbuffer-required'] = true;
      required.push(f.name);
    }

    properties[f.name] = prop;
  });

  return {
    '$schema': 'https://json-schema.org/draft/2020-12/schema',
    '$id': `https://spacedatastandards.org/schemas/${schema.name}.json`,
    title: `${schema.name} - ${schema.fullName}`,
    description: schema.description,
    type: 'object',
    'x-flatbuffer-root': true,
    'x-flatbuffer-file-identifier': schema.fileIdentifier || undefined,
    'x-flatbuffer-version': schema.version,
    properties,
    ...(required.length ? { required } : {}),
  };
}

// ============================================================================
// Downloads
// ============================================================================
function downloadSchema(format) {
  if (!currentSchema) return;

  let content, filename, mime;

  switch (format) {
    case 'json':
      content = JSON.stringify(generateJsonSchema(currentSchema), null, 2);
      filename = `${currentSchema.name}.schema.json`;
      mime = 'application/json';
      break;
    case 'fbs':
      content = generateFbs(currentSchema);
      filename = `${currentSchema.name}.fbs`;
      mime = 'text/plain';
      break;
    case 'ts':
      content = generateTS(currentSchema);
      filename = `${currentSchema.name}.ts`;
      mime = 'text/typescript';
      break;
    case 'go':
      content = generateGo(currentSchema);
      filename = `${currentSchema.name}.go`;
      mime = 'text/plain';
      break;
    case 'py':
      content = generatePython(currentSchema);
      filename = `${currentSchema.name}.py`;
      mime = 'text/python';
      break;
    case 'rs':
      content = generateRust(currentSchema);
      filename = `${currentSchema.name}.rs`;
      mime = 'text/plain';
      break;
    default: return;
  }

  downloadFile(content, filename, mime);
}

function downloadBulk(format) {
  // For a static site, generate individual files in a single download
  // In production this would be a server-generated zip
  const formats = format === 'all' ? ['json', 'fbs', 'ts', 'go', 'py', 'rs'] : [format === 'json' ? 'json' : format];

  // Generate a manifest of all schemas as JSON
  if (format === 'json' || format === 'all') {
    const allSchemas = {};
    SCHEMAS.forEach(s => {
      allSchemas[s.name] = generateJsonSchema(s);
    });
    const content = JSON.stringify(allSchemas, null, 2);
    downloadFile(content, `sds-schemas-all.json`, 'application/json');
    return;
  }

  // For other formats, generate a concatenated file
  let content = '';
  const ext = format === 'fbs' ? 'fbs' : format;
  SCHEMAS.forEach(s => {
    content += `// === ${s.name} - ${s.fullName} ===\n`;
    switch (format) {
      case 'fbs': content += generateFbs(s); break;
      case 'ts': content += generateTS(s); break;
    }
    content += '\n\n';
  });
  downloadFile(content, `sds-schemas-all.${ext}`, 'text/plain');
}

function downloadFile(content, filename, mime) {
  const blob = new Blob([content], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
  showToast(`Downloaded ${filename}`);
}

// ============================================================================
// Schema Diff View
// ============================================================================
function renderDiff() {
  // Placeholder diff between two "versions" showing example field changes
  const content = document.getElementById('diffContent');
  if (!currentSchema) return;

  content.innerHTML = `
    <div class="diff-line unchanged">  table ${currentSchema.name} {</div>
    ${currentSchema.fields.slice(0, 3).map(f =>
      `<div class="diff-line unchanged">    ${f.name}: ${f.fbType};</div>`
    ).join('')}
    <div class="diff-line added">+   NEW_FIELD: string;  // Added in v${currentSchema.version}</div>
    <div class="diff-line removed">-   DEPRECATED_FIELD: string;  // Removed</div>
    ${currentSchema.fields.slice(3, 6).map(f =>
      `<div class="diff-line unchanged">    ${f.name}: ${f.fbType};</div>`
    ).join('')}
    <div class="diff-line unchanged">  }</div>
  `;
}

// ============================================================================
// Documentation Toggles
// ============================================================================
function toggleDoc(id, btn) {
  const el = document.getElementById(id);
  const visible = el.style.display !== 'none';
  el.style.display = visible ? 'none' : '';
  btn.textContent = visible ? 'Show Guide' : 'Hide Guide';
}

function copyCode(btn) {
  const codeEl = btn.closest('.code-snippet').querySelector('pre code') ||
                  btn.closest('.code-snippet').querySelector('pre');
  if (codeEl) {
    navigator.clipboard.writeText(codeEl.textContent).then(() => showToast('Copied to clipboard'));
  }
}

// ============================================================================
// Toast
// ============================================================================
function showToast(message) {
  let toast = document.querySelector('.toast');
  if (!toast) {
    toast = document.createElement('div');
    toast.className = 'toast';
    toast.style.cssText = 'position:fixed;bottom:24px;left:50%;transform:translateX(-50%);padding:10px 24px;background:rgba(42,42,45,0.95);color:#F5F5F7;border:1px solid rgba(134,134,139,0.3);border-radius:12px;font-size:14px;z-index:99999;opacity:0;transition:opacity 0.3s;backdrop-filter:blur(10px);font-family:var(--font-sans);';
    document.body.appendChild(toast);
  }
  toast.textContent = message;
  toast.style.opacity = '1';
  setTimeout(() => { toast.style.opacity = '0'; }, 2000);
}

// ============================================================================
// API Simulation (client-side)
// ============================================================================

/**
 * Simulated API endpoints - these functions provide the same interface
 * as the planned server-side API, running entirely in the browser.
 */
const SchemaRegistryAPI = {
  /** GET /api/schemas - List all schemas */
  listSchemas() {
    return SCHEMAS.map(s => ({
      name: s.name,
      fullName: s.fullName,
      description: s.description,
      version: s.version,
      category: s.category,
      fieldCount: s.fields.length,
      formats: ['json-schema', 'flatbuffers', 'typescript', 'go', 'python', 'rust'],
      links: {
        json_schema: `/api/schemas/${s.name}/json-schema`,
        flatbuffers: `/api/schemas/${s.name}/flatbuffers`,
        typescript: `/api/schemas/${s.name}/typescript`,
      }
    }));
  },

  /** GET /api/schemas/{name} - Get schema metadata */
  getSchema(name) {
    const s = SCHEMAS.find(x => x.name === name.toUpperCase());
    if (!s) return { error: 'Schema not found', status: 404 };
    return {
      name: s.name,
      fullName: s.fullName,
      description: s.description,
      version: s.version,
      category: s.category,
      fieldCount: s.fields.length,
      includes: s.includes,
      fileIdentifier: s.fileIdentifier,
      formats: ['json-schema', 'flatbuffers', 'typescript', 'go', 'python', 'rust'],
    };
  },

  /** GET /api/schemas/{name}/json-schema - Get JSON Schema */
  getJsonSchema(name) {
    const s = SCHEMAS.find(x => x.name === name.toUpperCase());
    if (!s) return { error: 'Schema not found', status: 404 };
    return generateJsonSchema(s);
  },

  /** POST /api/validate - Validate data against schema */
  validate(schemaName, data) {
    const s = SCHEMAS.find(x => x.name === schemaName.toUpperCase());
    if (!s) return { valid: false, errors: ['Schema not found'], schema: schemaName };

    const errors = [];
    const requiredFields = s.fields.filter(f => f.required);
    requiredFields.forEach(f => {
      if (!(f.name in data)) {
        errors.push(`Missing required field: ${f.name}`);
      }
    });

    // Type checking
    Object.entries(data).forEach(([key, value]) => {
      const field = s.fields.find(f => f.name === key);
      if (!field) {
        errors.push(`Unknown field: ${key}`);
        return;
      }
      const expectedType = field.type;
      const actualType = Array.isArray(value) ? 'array' : typeof value;
      if (expectedType === 'number' && actualType !== 'number') {
        errors.push(`Field ${key}: expected number, got ${actualType}`);
      } else if (expectedType === 'integer' && (!Number.isInteger(value))) {
        errors.push(`Field ${key}: expected integer, got ${actualType}`);
      } else if (expectedType === 'string' && actualType !== 'string') {
        errors.push(`Field ${key}: expected string, got ${actualType}`);
      }
    });

    return {
      valid: errors.length === 0,
      errors,
      schema: schemaName,
      version: s.version,
    };
  },

  /** POST /api/generate - Generate code from schema */
  generate(schemaName, format) {
    const s = SCHEMAS.find(x => x.name === schemaName.toUpperCase());
    if (!s) return { error: 'Schema not found' };
    return { code: generateCode(s, format), format, schema: schemaName };
  },
};

// Expose API globally for console testing
window.SchemaRegistryAPI = SchemaRegistryAPI;

// ============================================================================
// Keyboard shortcuts
// ============================================================================
document.addEventListener('keydown', e => {
  if (e.key === 'Escape') closeSchemaModal();
});

// ============================================================================
// Initialize
// ============================================================================
document.addEventListener('DOMContentLoaded', () => {
  renderSchemaGrid();

  // Handle hash navigation
  if (window.location.hash) {
    const target = document.querySelector(window.location.hash);
    if (target) target.scrollIntoView({ behavior: 'smooth' });
  }
});
