/**
 * Space Data Standards - Code Generators
 * Generate code in multiple formats from schema definitions
 */

export function generateFbs(s) {
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

export function generateTS(s) {
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

export function generateGo(s) {
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

export function generatePython(s) {
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

export function generateRust(s) {
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

export function generateJsonSchema(schema) {
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

export function generateCode(schema, format) {
  switch (format) {
    case 'flatbuffers': return generateFbs(schema);
    case 'typescript': return generateTS(schema);
    case 'go': return generateGo(schema);
    case 'python': return generatePython(schema);
    case 'rust': return generateRust(schema);
    default: return '';
  }
}
