package appmanifest

import (
	"errors"
	"fmt"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/APP"
	flatbuffers "github.com/google/flatbuffers/go"
)

// $APP FlatBuffer lane (C4): ToAPP / FromAPP serialize an AppManifest to and
// from the published SDS $APP record (schema/APP/main.fbs, generated bindings
// in github.com/DigitalArsenal/spacedatastandards.org/lib/go/APP), size-prefixed
// per SDS convention (the same FinishSizePrefixed…/GetSizePrefixedRootAs… shape
// PNM/EPM use elsewhere in this repo).
//
// Field mapping is 1:1 with the AppManifest struct, which mirrors schema/APP
// field-for-field, so the mapping is mechanical:
//
//	AppManifest.ID          <-> APP.ID          Modules[] <-> MODULES[APPModuleRef]
//	AppManifest.Name        <-> APP.NAME        Data[]    <-> DATA[APPDataRef]
//	AppManifest.Version     <-> APP.VERSION     Sources[] <-> SOURCES[APPSourceRef]
//	AppManifest.Description <-> APP.DESCRIPTION  Pages[]   <-> UI[APPUIPage]
//	AppManifest.CreatedAt   <-> APP.CREATED_AT
//	AppManifest.UpdatedAt   <-> APP.UPDATED_AT
//
// The $APP lane is canonical on the Pages list (schema/APP has no single-UI
// field); the deprecated AppManifest.UI legacy entry lives only in the
// JSON/MBL lane and is not represented in $APP. New apps (the conjunction app)
// use Pages, so their $APP round-trip is a byte-for-byte / sha / enum-faithful
// identity — proven by the C4 acceptance tests.
//
// Alignment note (the SDS size-prefix gotcha): 8-byte scalars in APPModuleRef
// (MAX_WALL_CLOCK_MS / MAX_COST_UNITS uint64) must align relative to the
// size-prefixed buffer start. FinishSizePrefixedAPPBuffer and
// GetSizePrefixedRootAsAPP (which adds flatbuffers.SizeUint32 to every root
// offset) handle that entirely inside the generated bindings — we neither
// hand-compute nor hand-strip the size prefix.

// Enum name maps between the AppManifest string forms and the .fbs enum names
// used by APP.EnumValues*/APP.EnumNames*. These are the only non-mechanical
// part of the mapping (SourceKind "external-api" <-> "EXTERNAL_API" is not a
// pure case transform), so they are spelled out explicitly.
var (
	encToFBName = map[UIContentEncoding]string{
		EncodingUTF8:         "UTF8",
		EncodingBase64:       "BASE64",
		EncodingBase64Gzip:   "BASE64_GZIP",
		EncodingBase64Brotli: "BASE64_BROTLI",
	}
	encFromFBName = map[string]UIContentEncoding{
		"UTF8":          EncodingUTF8,
		"BASE64":        EncodingBase64,
		"BASE64_GZIP":   EncodingBase64Gzip,
		"BASE64_BROTLI": EncodingBase64Brotli,
	}
	dirToFBName = map[DataDirection]string{
		DataDirectionProduces: "PRODUCES",
		DataDirectionConsumes: "CONSUMES",
		DataDirectionBoth:     "BOTH",
	}
	dirFromFBName = map[string]DataDirection{
		"PRODUCES": DataDirectionProduces,
		"CONSUMES": DataDirectionConsumes,
		"BOTH":     DataDirectionBoth,
	}
	kindToFBName = map[SourceKind]string{
		SourceKindModule:      "MODULE",
		SourceKindExternalAPI: "EXTERNAL_API",
		SourceKindDataset:     "DATASET",
	}
	kindFromFBName = map[string]SourceKind{
		"MODULE":       SourceKindModule,
		"EXTERNAL_API": SourceKindExternalAPI,
		"DATASET":      SourceKindDataset,
	}
	runtimeToFBName = map[RuntimeTarget]string{
		RuntimeTargetNode: "NODE",
		RuntimeTargetPage: "PAGE",
		RuntimeTargetBoth: "BOTH",
	}
	runtimeFromFBName = map[string]RuntimeTarget{
		"NODE": RuntimeTargetNode,
		"PAGE": RuntimeTargetPage,
		"BOTH": RuntimeTargetBoth,
	}
	flowDirToFBName = map[FlowDirection]string{
		FlowDirectionToPage:        "TO_PAGE",
		FlowDirectionFromPage:      "FROM_PAGE",
		FlowDirectionBidirectional: "BIDIRECTIONAL",
	}
	flowDirFromFBName = map[string]FlowDirection{
		"TO_PAGE":       FlowDirectionToPage,
		"FROM_PAGE":     FlowDirectionFromPage,
		"BIDIRECTIONAL": FlowDirectionBidirectional,
	}
	flowTransportToFBName = map[FlowTransport]string{
		FlowTransportIPFSCID:      "IPFS_CID",
		FlowTransportPubsubTopic:  "PUBSUB_TOPIC",
		FlowTransportGatewayRoute: "GATEWAY_ROUTE",
	}
	flowTransportFromFBName = map[string]FlowTransport{
		"IPFS_CID":      FlowTransportIPFSCID,
		"PUBSUB_TOPIC":  FlowTransportPubsubTopic,
		"GATEWAY_ROUTE": FlowTransportGatewayRoute,
	}
)

// createStringOrZero returns a FlatBuffer string offset for non-empty s, or 0
// for empty s so the slot is omitted (an absent string reads back as nil ->
// "", identical to an empty one, keeping optional fields out of the buffer).
func createStringOrZero(b *flatbuffers.Builder, s string) flatbuffers.UOffsetT {
	if s == "" {
		return 0
	}
	return b.CreateString(s)
}

// ToAPP serializes the manifest to a size-prefixed $APP FlatBuffer using the
// published SDS bindings. It validates first, so a returned buffer always
// carries a well-formed, referentially-intact manifest.
func (a *AppManifest) ToAPP() ([]byte, error) {
	if a == nil {
		return nil, errors.New("app manifest is nil")
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}

	b := flatbuffers.NewBuilder(1024)

	modulesVec := buildModulesVector(b, a.Modules)
	dataVec := buildDataVector(b, a.Data)
	sourcesVec := buildSourcesVector(b, a.Sources)
	dataflowVec := buildDataflowVector(b, a.Dataflow)
	uiVec, err := buildUIVector(b, a.Pages)
	if err != nil {
		return nil, err
	}

	idOff := createStringOrZero(b, a.ID)
	nameOff := createStringOrZero(b, a.Name)
	versionOff := createStringOrZero(b, a.Version)
	descOff := createStringOrZero(b, a.Description)
	createdOff := createStringOrZero(b, a.CreatedAt)
	updatedOff := createStringOrZero(b, a.UpdatedAt)

	APP.APPStart(b)
	if idOff != 0 {
		APP.APPAddID(b, idOff)
	}
	if nameOff != 0 {
		APP.APPAddNAME(b, nameOff)
	}
	if versionOff != 0 {
		APP.APPAddVERSION(b, versionOff)
	}
	if descOff != 0 {
		APP.APPAddDESCRIPTION(b, descOff)
	}
	if modulesVec != 0 {
		APP.APPAddMODULES(b, modulesVec)
	}
	if dataVec != 0 {
		APP.APPAddDATA(b, dataVec)
	}
	if sourcesVec != 0 {
		APP.APPAddSOURCES(b, sourcesVec)
	}
	if uiVec != 0 {
		APP.APPAddUI(b, uiVec)
	}
	if createdOff != 0 {
		APP.APPAddCREATED_AT(b, createdOff)
	}
	if updatedOff != 0 {
		APP.APPAddUPDATED_AT(b, updatedOff)
	}
	if dataflowVec != 0 {
		APP.APPAddDATAFLOW(b, dataflowVec)
	}
	root := APP.APPEnd(b)

	APP.FinishSizePrefixedAPPBuffer(b, root)
	return b.FinishedBytes(), nil
}

func buildModulesVector(b *flatbuffers.Builder, modules []ModuleRef) flatbuffers.UOffsetT {
	if len(modules) == 0 {
		return 0
	}
	offs := make([]flatbuffers.UOffsetT, len(modules))
	for i, m := range modules {
		idOff := createStringOrZero(b, m.ID)
		pluginOff := createStringOrZero(b, m.PluginID)
		hashOff := createStringOrZero(b, m.ContentHash)
		verOff := createStringOrZero(b, m.Version)
		roleOff := createStringOrZero(b, m.Role)
		descOff := createStringOrZero(b, m.Description)

		APP.APPModuleRefStart(b)
		if idOff != 0 {
			APP.APPModuleRefAddID(b, idOff)
		}
		if pluginOff != 0 {
			APP.APPModuleRefAddPLUGIN_ID(b, pluginOff)
		}
		if hashOff != 0 {
			APP.APPModuleRefAddCONTENT_HASH(b, hashOff)
		}
		if verOff != 0 {
			APP.APPModuleRefAddVERSION(b, verOff)
		}
		if roleOff != 0 {
			APP.APPModuleRefAddROLE(b, roleOff)
		}
		if descOff != 0 {
			APP.APPModuleRefAddDESCRIPTION(b, descOff)
		}
		// RuntimeTarget: "" and "node" both map to the .fbs default NODE(0),
		// which the generated Add skips — so an omitted target reads back as
		// NODE, matching the .fbs default semantics.
		if fbName, ok := runtimeToFBName[m.RuntimeTarget]; ok {
			APP.APPModuleRefAddRUNTIME_TARGET(b, APP.EnumValuesappRuntimeTarget[fbName])
		}
		offs[i] = APP.APPModuleRefEnd(b)
	}
	APP.APPStartMODULESVector(b, len(offs))
	for i := len(offs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	return b.EndVector(len(offs))
}

func buildDataVector(b *flatbuffers.Builder, data []DataRef) flatbuffers.UOffsetT {
	if len(data) == 0 {
		return 0
	}
	offs := make([]flatbuffers.UOffsetT, len(data))
	for i, d := range data {
		idOff := createStringOrZero(b, d.ID)
		typeOff := createStringOrZero(b, d.SDSType)
		moduleOff := createStringOrZero(b, d.ModuleID)
		descOff := createStringOrZero(b, d.Description)

		APP.APPDataRefStart(b)
		if idOff != 0 {
			APP.APPDataRefAddID(b, idOff)
		}
		if typeOff != 0 {
			APP.APPDataRefAddSDS_TYPE(b, typeOff)
		}
		// Direction: "" and "produces" both map to the .fbs default PRODUCES(0),
		// which the generated Add skips — so an omitted direction reads back as
		// PRODUCES, matching the .fbs default semantics.
		if fbName, ok := dirToFBName[d.Direction]; ok {
			APP.APPDataRefAddDIRECTION(b, APP.EnumValuesappDataDirection[fbName])
		}
		if moduleOff != 0 {
			APP.APPDataRefAddMODULE_ID(b, moduleOff)
		}
		if descOff != 0 {
			APP.APPDataRefAddDESCRIPTION(b, descOff)
		}
		offs[i] = APP.APPDataRefEnd(b)
	}
	APP.APPStartDATAVector(b, len(offs))
	for i := len(offs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	return b.EndVector(len(offs))
}

func buildSourcesVector(b *flatbuffers.Builder, sources []SourceRef) flatbuffers.UOffsetT {
	if len(sources) == 0 {
		return 0
	}
	offs := make([]flatbuffers.UOffsetT, len(sources))
	for i, s := range sources {
		idOff := createStringOrZero(b, s.ID)
		refOff := createStringOrZero(b, s.Ref)
		descOff := createStringOrZero(b, s.Description)

		APP.APPSourceRefStart(b)
		if idOff != 0 {
			APP.APPSourceRefAddID(b, idOff)
		}
		if fbName, ok := kindToFBName[s.Kind]; ok {
			APP.APPSourceRefAddKIND(b, APP.EnumValuesappSourceKind[fbName])
		}
		if refOff != 0 {
			APP.APPSourceRefAddREF(b, refOff)
		}
		if descOff != 0 {
			APP.APPSourceRefAddDESCRIPTION(b, descOff)
		}
		offs[i] = APP.APPSourceRefEnd(b)
	}
	APP.APPStartSOURCESVector(b, len(offs))
	for i := len(offs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	return b.EndVector(len(offs))
}

func buildDataflowVector(b *flatbuffers.Builder, flows []DataflowEntry) flatbuffers.UOffsetT {
	if len(flows) == 0 {
		return 0
	}
	offs := make([]flatbuffers.UOffsetT, len(flows))
	for i, f := range flows {
		nameOff := createStringOrZero(b, f.Name)
		schemaOff := createStringOrZero(b, f.SDSSchema)
		locatorOff := createStringOrZero(b, f.Locator)
		moduleOff := createStringOrZero(b, f.ModuleID)
		methodOff := createStringOrZero(b, f.MethodID)
		portOff := createStringOrZero(b, f.PortId)
		descOff := createStringOrZero(b, f.Description)

		APP.APPDataflowStart(b)
		if nameOff != 0 {
			APP.APPDataflowAddNAME(b, nameOff)
		}
		// Direction: "" and "to_page" both map to the .fbs default TO_PAGE(0),
		// which the generated Add skips — so an omitted direction reads back as
		// TO_PAGE.
		if fbName, ok := flowDirToFBName[f.Direction]; ok {
			APP.APPDataflowAddDIRECTION(b, APP.EnumValuesappFlowDirection[fbName])
		}
		if schemaOff != 0 {
			APP.APPDataflowAddSDS_SCHEMA(b, schemaOff)
		}
		// Transport: "" and "ipfs_cid" both map to the .fbs default IPFS_CID(0),
		// which the generated Add skips — so an omitted transport reads back as
		// IPFS_CID.
		if fbName, ok := flowTransportToFBName[f.Transport]; ok {
			APP.APPDataflowAddTRANSPORT(b, APP.EnumValuesappFlowTransport[fbName])
		}
		if locatorOff != 0 {
			APP.APPDataflowAddLOCATOR(b, locatorOff)
		}
		if moduleOff != 0 {
			APP.APPDataflowAddMODULE_ID(b, moduleOff)
		}
		if methodOff != 0 {
			APP.APPDataflowAddMETHOD_ID(b, methodOff)
		}
		if portOff != 0 {
			APP.APPDataflowAddPORT_ID(b, portOff)
		}
		// ContentEncoding: "" and "utf8" both map to the .fbs default UTF8(0),
		// which the generated Add skips — so an omitted encoding reads back as
		// UTF8.
		if fbName, ok := encToFBName[f.ContentEncoding.normalize()]; ok {
			APP.APPDataflowAddCONTENT_ENCODING(b, APP.EnumValuesappContentEncoding[fbName])
		}
		if descOff != 0 {
			APP.APPDataflowAddDESCRIPTION(b, descOff)
		}
		offs[i] = APP.APPDataflowEnd(b)
	}
	APP.APPStartDATAFLOWVector(b, len(offs))
	for i := len(offs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	return b.EndVector(len(offs))
}

func buildUIVector(b *flatbuffers.Builder, pages []UIPage) (flatbuffers.UOffsetT, error) {
	if len(pages) == 0 {
		return 0, nil
	}
	offs := make([]flatbuffers.UOffsetT, len(pages))
	for i, p := range pages {
		if p.IsInline() && !p.Encoding.valid() {
			return 0, fmt.Errorf("app manifest: pages[%d] (%s): unknown content encoding %q", i, p.ID, p.Encoding)
		}
		idOff := createStringOrZero(b, p.ID)
		titleOff := createStringOrZero(b, p.Title)
		descOff := createStringOrZero(b, p.Description)
		iconOff := createStringOrZero(b, p.Icon)
		colorOff := createStringOrZero(b, p.Color)
		textColorOff := createStringOrZero(b, p.TextColor)
		contentOff := createStringOrZero(b, p.Content)
		mediaOff := createStringOrZero(b, p.MediaType)
		shaOff := createStringOrZero(b, p.ContentSHA256)
		moduleOff := createStringOrZero(b, p.ModuleID)
		urlOff := createStringOrZero(b, p.URL)

		APP.APPUIPageStart(b)
		if idOff != 0 {
			APP.APPUIPageAddID(b, idOff)
		}
		if titleOff != 0 {
			APP.APPUIPageAddTITLE(b, titleOff)
		}
		if descOff != 0 {
			APP.APPUIPageAddDESCRIPTION(b, descOff)
		}
		if iconOff != 0 {
			APP.APPUIPageAddICON(b, iconOff)
		}
		if colorOff != 0 {
			APP.APPUIPageAddCOLOR(b, colorOff)
		}
		if textColorOff != 0 {
			APP.APPUIPageAddTEXT_COLOR(b, textColorOff)
		}
		if contentOff != 0 {
			APP.APPUIPageAddCONTENT(b, contentOff)
		}
		// Encoding: "" / "utf8" both map to the .fbs default UTF8(0), which the
		// generated Add skips, so an omitted encoding reads back as UTF8.
		if fbName, ok := encToFBName[p.Encoding.normalize()]; ok {
			APP.APPUIPageAddENCODING(b, APP.EnumValuesappContentEncoding[fbName])
		}
		if mediaOff != 0 {
			APP.APPUIPageAddMEDIA_TYPE(b, mediaOff)
		}
		if shaOff != 0 {
			APP.APPUIPageAddCONTENT_SHA256(b, shaOff)
		}
		if p.Entry {
			APP.APPUIPageAddENTRY(b, true)
		}
		if moduleOff != 0 {
			APP.APPUIPageAddMODULE_ID(b, moduleOff)
		}
		if urlOff != 0 {
			APP.APPUIPageAddURL(b, urlOff)
		}
		offs[i] = APP.APPUIPageEnd(b)
	}
	APP.APPStartUIVector(b, len(offs))
	for i := len(offs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	return b.EndVector(len(offs)), nil
}

// FromAPP is the inverse of ToAPP: it parses a size-prefixed $APP FlatBuffer
// into an AppManifest and validates it. buf may be untrusted (peer-delivered),
// so every accessor is guarded by recover — a crafted/corrupt buffer produces
// an error, never a panic (the same posture FromMBL takes).
func FromAPP(buf []byte) (manifest *AppManifest, err error) {
	defer func() {
		if r := recover(); r != nil {
			manifest = nil
			err = fmt.Errorf("malformed $APP flatbuffer: %v", r)
		}
	}()

	if len(buf) < 8 || !APP.SizePrefixedAPPBufferHasIdentifier(buf) {
		return nil, errors.New("buffer does not carry the size-prefixed $APP file identifier")
	}
	root := APP.GetSizePrefixedRootAsAPP(buf, 0)

	m := &AppManifest{
		ID:          string(root.ID()),
		Name:        string(root.NAME()),
		Version:     string(root.VERSION()),
		Description: string(root.DESCRIPTION()),
		CreatedAt:   string(root.CREATED_AT()),
		UpdatedAt:   string(root.UPDATED_AT()),
	}

	if n := root.ModulesLength(); n > 0 {
		m.Modules = make([]ModuleRef, 0, n)
		for i := 0; i < n; i++ {
			var mr APP.APPModuleRef
			if !root.Modules(&mr, i) {
				continue
			}
			m.Modules = append(m.Modules, ModuleRef{
				ID:            string(mr.ID()),
				PluginID:      string(mr.PLUGIN_ID()),
				ContentHash:   string(mr.CONTENT_HASH()),
				Version:       string(mr.VERSION()),
				Role:          string(mr.ROLE()),
				Description:   string(mr.DESCRIPTION()),
				RuntimeTarget: runtimeFromFBName[mr.RUNTIME_TARGET().String()],
			})
		}
	}

	if n := root.DATALength(); n > 0 {
		m.Data = make([]DataRef, 0, n)
		for i := 0; i < n; i++ {
			var dr APP.APPDataRef
			if !root.Data(&dr, i) {
				continue
			}
			m.Data = append(m.Data, DataRef{
				ID:          string(dr.ID()),
				SDSType:     string(dr.SDS_TYPE()),
				Direction:   dirFromFBName[dr.DIRECTION().String()],
				ModuleID:    string(dr.MODULE_ID()),
				Description: string(dr.DESCRIPTION()),
			})
		}
	}

	if n := root.SOURCESLength(); n > 0 {
		m.Sources = make([]SourceRef, 0, n)
		for i := 0; i < n; i++ {
			var sr APP.APPSourceRef
			if !root.Sources(&sr, i) {
				continue
			}
			m.Sources = append(m.Sources, SourceRef{
				ID:          string(sr.ID()),
				Kind:        kindFromFBName[sr.KIND().String()],
				Ref:         string(sr.REF()),
				Description: string(sr.DESCRIPTION()),
			})
		}
	}

	if n := root.UILength(); n > 0 {
		m.Pages = make([]UIPage, 0, n)
		for i := 0; i < n; i++ {
			var p APP.APPUIPage
			if !root.Ui(&p, i) {
				continue
			}
			content := string(p.CONTENT())
			// Encoding is only meaningful for an inline page. For a
			// module-served page (no CONTENT) leave it empty rather than
			// letting the .fbs UTF8 default surface as a spurious "utf8".
			var enc UIContentEncoding
			if content != "" {
				enc = encFromFBName[p.ENCODING().String()]
			}
			m.Pages = append(m.Pages, UIPage{
				ID:            string(p.ID()),
				Title:         string(p.TITLE()),
				Description:   string(p.DESCRIPTION()),
				Icon:          string(p.ICON()),
				Color:         string(p.COLOR()),
				TextColor:     string(p.TEXT_COLOR()),
				Content:       content,
				Encoding:      enc,
				MediaType:     string(p.MEDIA_TYPE()),
				ContentSHA256: string(p.CONTENT_SHA256()),
				Entry:         p.ENTRY(),
				ModuleID:      string(p.MODULE_ID()),
				URL:           string(p.URL()),
			})
		}
	}

	if n := root.DATAFLOWLength(); n > 0 {
		m.Dataflow = make([]DataflowEntry, 0, n)
		for i := 0; i < n; i++ {
			var df APP.APPDataflow
			if !root.Dataflow(&df, i) {
				continue
			}
			m.Dataflow = append(m.Dataflow, DataflowEntry{
				Name:            string(df.NAME()),
				Direction:       flowDirFromFBName[df.DIRECTION().String()],
				SDSSchema:       string(df.SDS_SCHEMA()),
				Transport:       flowTransportFromFBName[df.TRANSPORT().String()],
				Locator:         string(df.LOCATOR()),
				ModuleID:        string(df.MODULE_ID()),
				MethodID:        string(df.METHOD_ID()),
				PortId:          string(df.PORT_ID()),
				ContentEncoding: encFromFBName[df.CONTENT_ENCODING().String()],
				Description:     string(df.DESCRIPTION()),
			})
		}
	}

	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}
