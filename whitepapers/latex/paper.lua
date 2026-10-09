-- Typesets an SDN whitepaper's markdown as LaTeX. The markdown under
-- whitepapers/ stays the only copy of the text: the title block is whatever
-- precedes the first "##" heading, figures are the TikZ PDFs built from
-- whitepapers/figures/, and links to a sibling paper's markdown open its PDF.

local function latex(s) return pandoc.RawBlock('latex', s) end
local function strip(html) return (html:gsub('<[^>]+>', ''):gsub('&quot;', '"'):gsub('&amp;', '&'):gsub('^%s+', ''):gsub('%s+$', '')) end
local function escape(s) return (s:gsub('([#$%%&_{}])', '\\%1'):gsub('~', '\\textasciitilde{}'):gsub('%^', '\\textasciicircum{}')) end

local function figure(src, width)
  local name = src:match('assets/([%w%-]+)%.svg$')
  if not name then return nil end
  local frac = math.min(1, (tonumber(width) or 720) / 720)
  return latex(string.format('\\begin{center}\\includegraphics[width=%.2f\\linewidth]{%s.pdf}\\end{center}', frac, name))
end

local function html_image(raw)
  local src = raw:match('src="([^"]+)"')
  if src then return figure(src, raw:match('width="(%d+)"')) end
end

local function quotes(s) return (s:gsub('"([^"]*)"', "``%1''")) end

local function title_block(blocks)
  local title, lines, logo = nil, {}, nil
  for _, b in ipairs(blocks) do
    if b.t == 'Header' and b.level == 1 then
      title = pandoc.write(pandoc.Pandoc({pandoc.Plain(b.content)}), 'latex')
    elseif b.t == 'RawBlock' and b.format == 'html' then
      -- One raw block may hold several elements; take them in order.
      for tag in b.text:gmatch('(<(%w+)[^>]*>.-</%2>)') do
        if tag:match('<img') then logo = html_image(tag)
        elseif tag:match('^<h1') then title = quotes(escape(strip(tag)))
        elseif tag:match('^<p') and strip(tag) ~= '' then table.insert(lines, quotes(escape(strip(tag)))) end
      end
      if not b.text:match('</') and b.text:match('<img') then logo = html_image(b.text) end
    elseif b.t == 'Para' or b.t == 'Plain' then
      table.insert(lines, pandoc.write(pandoc.Pandoc({pandoc.Plain(b.content)}), 'latex'))
    end
  end
  local out = {}
  if logo then table.insert(out, logo) end
  local body = {'\\begin{center}', '{\\LARGE\\bfseries ' .. (title or '') .. '\\par}\\vspace{0.8em}'}
  for i, l in ipairs(lines) do
    table.insert(body, (i == 1 and '{\\large ' or '{\\small ') .. l .. '\\par}\\vspace{0.35em}')
  end
  table.insert(body, '\\end{center}\\vspace{1em}')
  table.insert(out, latex(table.concat(body, '\n')))
  return out
end

local function Pandoc(doc)
  local front, rest, seen = {}, {}, false
  for _, b in ipairs(doc.blocks) do
    if not seen and b.t == 'Header' and b.level == 2 then seen = true end
    if b.t == 'HorizontalRule' then -- section separators on GitHub; a typeset paper has none
    elseif seen then table.insert(rest, b) else table.insert(front, b) end
  end
  local blocks = title_block(front)
  for _, b in ipairs(rest) do table.insert(blocks, b) end
  doc.blocks = blocks
  return doc
end

local function RawBlock(b)
  if b.format == 'html' then
    if b.text:match('<img') then return html_image(b.text) or {} end
    local t = strip(b.text)
    if t == '' then return {} end
    return pandoc.Para({pandoc.Str(t)})
  end
end

local function RawInline(r)
  if r.format ~= 'html' then return nil end
  if r.text == '<sup>' then return pandoc.RawInline('latex', '\\textsuperscript{') end
  if r.text == '</sup>' then return pandoc.RawInline('latex', '}') end
  local id = r.text:match('^<a id="([^"]+)"')
  if id then return pandoc.RawInline('latex', '\\hypertarget{' .. id .. '}{}') end
  if r.text:match('^</a>') or r.text:match('^<br') then return {} end
end

local function Image(img)
  local name = img.src:match('assets/([%w%-]+)%.svg$')
  if name then img.src = name .. '.pdf' end
  return img
end

local function Link(l)
  local file, frag = l.target:match('^([%w%-]+)%.md(#?.*)$')
  if file then l.target = file .. '.pdf' end
  return l
end

-- A table wider than the line gets column widths in proportion to its
-- longest cells (capped), so LaTeX wraps the long ones instead of running
-- them off the page.
local LINE = 78
local function Table(tbl)
  local n = #tbl.colspecs
  local longest = {}
  for i = 1, n do longest[i] = 4 end
  local function visit(rows)
    for _, row in ipairs(rows) do
      for i, cell in ipairs(row.cells) do
        if i <= n then longest[i] = math.max(longest[i], math.min(60, #pandoc.utils.stringify(cell.contents))) end
      end
    end
  end
  visit(tbl.head.rows)
  for _, body in ipairs(tbl.bodies) do visit(body.body) end
  local total = 0
  for i = 1, n do total = total + longest[i] + 3 end
  if total <= LINE then return nil end
  for i = 1, n do tbl.colspecs[i] = {tbl.colspecs[i][1], (longest[i] + 3) / total * 0.98} end
  return tbl
end

-- Primes become math primes: Latin Modern has no text glyph for them, and
-- mapping the characters clashes with the math setup.
local PRIMES = { ['′'] = "{}^{\\prime}", ['″'] = "{}^{\\prime\\prime}" }
local function Str(el)
  local out, rest, found = {}, el.text, false
  while #rest > 0 do
    local at, mark
    for m in pairs(PRIMES) do
      local i = rest:find(m, 1, true)
      if i and (not at or i < at) then at, mark = i, m end
    end
    if not at then table.insert(out, pandoc.Str(rest)); break end
    found = true
    if at > 1 then table.insert(out, pandoc.Str(rest:sub(1, at - 1))) end
    table.insert(out, pandoc.Math('InlineMath', PRIMES[mark]))
    rest = rest:sub(at + #mark)
  end
  if found then return out end
end

-- The title block reads the raw HTML header before the element rules rewrite it.
return {{Pandoc = Pandoc}, {RawBlock = RawBlock, RawInline = RawInline, Image = Image, Link = Link, Table = Table, Str = Str}}
