/**
 * XTCE Type Definitions
 * Based on CCSDS 660.1-G-2 Standard
 */

// ============================================================================
// XTCE Parameter Types
// ============================================================================

export interface XTCEIntegerParameterType {
  '@_name': string;
  '@_signed'?: string;
  '@_sizeInBits'?: string;
  '@_shortDescription'?: string;
  UnitSet?: XTCEUnitSet;
  IntegerDataEncoding?: XTCEIntegerDataEncoding;
  ValidRange?: XTCEValidRange;
}

export interface XTCEFloatParameterType {
  '@_name': string;
  '@_sizeInBits'?: string;
  '@_shortDescription'?: string;
  UnitSet?: XTCEUnitSet;
  FloatDataEncoding?: XTCEFloatDataEncoding;
  IntegerDataEncoding?: XTCEIntegerDataEncoding; // For calibrated values
  ValidRange?: XTCEValidRange;
  DefaultCalibrator?: XTCECalibrator;
}

export interface XTCEStringParameterType {
  '@_name': string;
  '@_shortDescription'?: string;
  StringDataEncoding?: XTCEStringDataEncoding;
}

export interface XTCEEnumeratedParameterType {
  '@_name': string;
  '@_shortDescription'?: string;
  IntegerDataEncoding?: XTCEIntegerDataEncoding;
  EnumerationList?: XTCEEnumerationList;
}

export interface XTCEBooleanParameterType {
  '@_name': string;
  '@_zeroStringValue'?: string;
  '@_oneStringValue'?: string;
  '@_shortDescription'?: string;
  IntegerDataEncoding?: XTCEIntegerDataEncoding;
}

export interface XTCEAbsoluteTimeParameterType {
  '@_name': string;
  '@_shortDescription'?: string;
  Encoding?: XTCETimeEncoding;
  ReferenceTime?: XTCEReferenceTime;
}

export interface XTCEArrayParameterType {
  '@_name': string;
  '@_shortDescription'?: string;
  '@_arrayTypeRef': string;
  DimensionList?: XTCEDimensionList;
}

export interface XTCEBinaryParameterType {
  '@_name': string;
  '@_shortDescription'?: string;
  BinaryDataEncoding?: XTCEBinaryDataEncoding;
}

// ============================================================================
// XTCE Data Encodings
// ============================================================================

export interface XTCEIntegerDataEncoding {
  '@_sizeInBits'?: string;
  '@_encoding'?: 'unsigned' | 'signMagnitude' | 'twosComplement' | 'onesComplement' | 'BCD' | 'packedBCD';
  '@_bitOrder'?: 'mostSignificantBitFirst' | 'leastSignificantBitFirst';
  '@_changeThreshold'?: string;
  DefaultCalibrator?: XTCECalibrator;
}

export interface XTCEFloatDataEncoding {
  '@_sizeInBits'?: string;
  '@_encoding'?: 'IEEE754_1985' | 'MILSTD_1750A';
  '@_bitOrder'?: 'mostSignificantBitFirst' | 'leastSignificantBitFirst';
  '@_changeThreshold'?: string;
}

export interface XTCEStringDataEncoding {
  '@_encoding'?: 'US-ASCII' | 'UTF-8' | 'UTF-16';
  SizeInBits?: XTCESizeInBits;
}

export interface XTCEBinaryDataEncoding {
  SizeInBits?: XTCESizeInBits;
}

export interface XTCETimeEncoding {
  IntegerDataEncoding?: XTCEIntegerDataEncoding;
  FloatDataEncoding?: XTCEFloatDataEncoding;
}

// ============================================================================
// XTCE Supporting Types
// ============================================================================

export interface XTCEUnitSet {
  Unit?: XTCEUnit | XTCEUnit[];
}

export interface XTCEUnit {
  '@_description'?: string;
  '@_factor'?: string;
  '@_power'?: string;
  '#text'?: string;
}

export interface XTCEValidRange {
  '@_minInclusive'?: string;
  '@_maxInclusive'?: string;
  '@_minExclusive'?: string;
  '@_maxExclusive'?: string;
}

export interface XTCEEnumerationList {
  Enumeration?: XTCEEnumeration | XTCEEnumeration[];
}

export interface XTCEEnumeration {
  '@_value': string;
  '@_label': string;
  '@_shortDescription'?: string;
}

export interface XTCESizeInBits {
  Fixed?: XTCEFixed;
  TerminationChar?: XTCETerminationChar;
  LeadingSize?: XTCELeadingSize;
}

export interface XTCEFixed {
  FixedValue?: string;
}

export interface XTCETerminationChar {
  '@_terminationChar'?: string;
}

export interface XTCELeadingSize {
  '@_sizeInBitsOfSizeTag'?: string;
}

export interface XTCECalibrator {
  PolynomialCalibrator?: XTCEPolynomialCalibrator;
  SplineCalibrator?: XTCESplineCalibrator;
  MathOperationCalibrator?: XTCEMathOperationCalibrator;
}

export interface XTCEPolynomialCalibrator {
  Term?: XTCETerm | XTCETerm[];
}

export interface XTCETerm {
  '@_coefficient': string;
  '@_exponent': string;
}

export interface XTCESplineCalibrator {
  SplinePoint?: XTCESplinePoint | XTCESplinePoint[];
}

export interface XTCESplinePoint {
  '@_raw': string;
  '@_calibrated': string;
}

export interface XTCEMathOperationCalibrator {
  // Math expression calibrator (typically uses RPN or infix notation)
  '@_algorithm'?: string;
}

export interface XTCEReferenceTime {
  Epoch?: string;
  OffsetFrom?: XTCEOffsetFrom;
}

export interface XTCEOffsetFrom {
  '@_parameterRef'?: string;
}

export interface XTCEDimensionList {
  Dimension?: XTCEDimension | XTCEDimension[];
}

export interface XTCEDimension {
  StartingIndex?: XTCEStartingIndex;
  EndingIndex?: XTCEEndingIndex;
}

export interface XTCEStartingIndex {
  FixedValue?: string;
}

export interface XTCEEndingIndex {
  FixedValue?: string;
}

// ============================================================================
// XTCE Parameter
// ============================================================================

export interface XTCEParameter {
  '@_name': string;
  '@_parameterTypeRef': string;
  '@_shortDescription'?: string;
  ParameterProperties?: XTCEParameterProperties;
  AncillaryDataSet?: XTCEAncillaryDataSet;
  AliasSet?: XTCEAliasSet;
}

export interface XTCEParameterProperties {
  '@_dataSource'?: 'telemetered' | 'derived' | 'constant' | 'local';
  '@_readOnly'?: string;
  SystemName?: string;
}

export interface XTCEAncillaryDataSet {
  AncillaryData?: XTCEAncillaryData | XTCEAncillaryData[];
}

export interface XTCEAncillaryData {
  '@_name': string;
  '#text'?: string;
}

export interface XTCEAliasSet {
  Alias?: XTCEAlias | XTCEAlias[];
}

export interface XTCEAlias {
  '@_nameSpace': string;
  '@_alias': string;
}

// ============================================================================
// XTCE Argument Types (for Commands)
// ============================================================================

export interface XTCEIntegerArgumentType extends XTCEIntegerParameterType {}
export interface XTCEFloatArgumentType extends XTCEFloatParameterType {}
export interface XTCEStringArgumentType extends XTCEStringParameterType {}
export interface XTCEEnumeratedArgumentType extends XTCEEnumeratedParameterType {}
export interface XTCEBooleanArgumentType extends XTCEBooleanParameterType {}
export interface XTCEBinaryArgumentType extends XTCEBinaryParameterType {}

export interface XTCEArgument {
  '@_name': string;
  '@_argumentTypeRef': string;
  '@_shortDescription'?: string;
}

// ============================================================================
// XTCE Container and Command Types
// ============================================================================

export interface XTCESequenceContainer {
  '@_name': string;
  '@_abstract'?: string;
  '@_shortDescription'?: string;
  EntryList?: XTCEEntryList;
  BaseContainer?: XTCEBaseContainer;
  DefaultRateInStream?: XTCEDefaultRateInStream;
}

export interface XTCEEntryList {
  ParameterRefEntry?: XTCEParameterRefEntry | XTCEParameterRefEntry[];
  ContainerRefEntry?: XTCEContainerRefEntry | XTCEContainerRefEntry[];
  ArrayParameterRefEntry?: XTCEArrayParameterRefEntry | XTCEArrayParameterRefEntry[];
}

export interface XTCEParameterRefEntry {
  '@_parameterRef': string;
  LocationInContainerInBits?: XTCELocationInContainerInBits;
}

export interface XTCEContainerRefEntry {
  '@_containerRef': string;
}

export interface XTCEArrayParameterRefEntry {
  '@_parameterRef': string;
}

export interface XTCEBaseContainer {
  '@_containerRef': string;
  RestrictionCriteria?: XTCERestrictionCriteria;
}

export interface XTCELocationInContainerInBits {
  '@_referenceLocation'?: 'containerStart' | 'previousEntry' | 'nextEntry' | 'containerEnd';
  FixedValue?: string;
}

export interface XTCEDefaultRateInStream {
  '@_basis'?: 'perSecond' | 'perContainerUpdate';
  '@_minimumValue'?: string;
  '@_maximumValue'?: string;
}

export interface XTCERestrictionCriteria {
  Comparison?: XTCEComparison | XTCEComparison[];
  ComparisonList?: XTCEComparisonList;
}

export interface XTCEComparison {
  '@_parameterRef': string;
  '@_value': string;
  '@_comparisonOperator'?: '==' | '!=' | '<' | '<=' | '>' | '>=';
}

export interface XTCEComparisonList {
  Comparison?: XTCEComparison | XTCEComparison[];
}

export interface XTCEMetaCommand {
  '@_name': string;
  '@_abstract'?: string;
  '@_shortDescription'?: string;
  BaseMetaCommand?: XTCEBaseMetaCommand;
  ArgumentList?: XTCEArgumentList;
  CommandContainer?: XTCECommandContainer;
}

export interface XTCEBaseMetaCommand {
  '@_metaCommandRef': string;
  ArgumentAssignmentList?: XTCEArgumentAssignmentList;
}

export interface XTCEArgumentList {
  Argument?: XTCEArgument | XTCEArgument[];
}

export interface XTCEArgumentAssignmentList {
  ArgumentAssignment?: XTCEArgumentAssignment | XTCEArgumentAssignment[];
}

export interface XTCEArgumentAssignment {
  '@_argumentName': string;
  '@_argumentValue': string;
}

export interface XTCECommandContainer {
  '@_name': string;
  EntryList?: XTCECommandEntryList;
  BaseContainer?: XTCEBaseContainer;
}

export interface XTCECommandEntryList {
  ArgumentRefEntry?: XTCEArgumentRefEntry | XTCEArgumentRefEntry[];
  ParameterRefEntry?: XTCEParameterRefEntry | XTCEParameterRefEntry[];
}

export interface XTCEArgumentRefEntry {
  '@_argumentRef': string;
}

// ============================================================================
// XTCE Document Structure
// ============================================================================

export interface XTCEParameterTypeSet {
  IntegerParameterType?: XTCEIntegerParameterType | XTCEIntegerParameterType[];
  FloatParameterType?: XTCEFloatParameterType | XTCEFloatParameterType[];
  StringParameterType?: XTCEStringParameterType | XTCEStringParameterType[];
  EnumeratedParameterType?: XTCEEnumeratedParameterType | XTCEEnumeratedParameterType[];
  BooleanParameterType?: XTCEBooleanParameterType | XTCEBooleanParameterType[];
  AbsoluteTimeParameterType?: XTCEAbsoluteTimeParameterType | XTCEAbsoluteTimeParameterType[];
  ArrayParameterType?: XTCEArrayParameterType | XTCEArrayParameterType[];
  BinaryParameterType?: XTCEBinaryParameterType | XTCEBinaryParameterType[];
}

export interface XTCEArgumentTypeSet {
  IntegerArgumentType?: XTCEIntegerArgumentType | XTCEIntegerArgumentType[];
  FloatArgumentType?: XTCEFloatArgumentType | XTCEFloatArgumentType[];
  StringArgumentType?: XTCEStringArgumentType | XTCEStringArgumentType[];
  EnumeratedArgumentType?: XTCEEnumeratedArgumentType | XTCEEnumeratedArgumentType[];
  BooleanArgumentType?: XTCEBooleanArgumentType | XTCEBooleanArgumentType[];
  BinaryArgumentType?: XTCEBinaryArgumentType | XTCEBinaryArgumentType[];
}

export interface XTCEParameterSet {
  Parameter?: XTCEParameter | XTCEParameter[];
}

export interface XTCEContainerSet {
  SequenceContainer?: XTCESequenceContainer | XTCESequenceContainer[];
}

export interface XTCEMetaCommandSet {
  MetaCommand?: XTCEMetaCommand | XTCEMetaCommand[];
}

export interface XTCETelemetryMetaData {
  ParameterTypeSet?: XTCEParameterTypeSet;
  ParameterSet?: XTCEParameterSet;
  ContainerSet?: XTCEContainerSet;
  MessageSet?: unknown;
  StreamSet?: unknown;
  AlgorithmSet?: unknown;
}

export interface XTCECommandMetaData {
  ParameterTypeSet?: XTCEParameterTypeSet;
  ArgumentTypeSet?: XTCEArgumentTypeSet;
  ParameterSet?: XTCEParameterSet;
  MetaCommandSet?: XTCEMetaCommandSet;
}

export interface XTCESpaceSystem {
  '@_name': string;
  '@_xmlns:xtce'?: string;
  '@_shortDescription'?: string;
  TelemetryMetaData?: XTCETelemetryMetaData;
  CommandMetaData?: XTCECommandMetaData;
  SpaceSystem?: XTCESpaceSystem | XTCESpaceSystem[];
}

export interface XTCEDocument {
  SpaceSystem?: XTCESpaceSystem;
  'xtce:SpaceSystem'?: XTCESpaceSystem;
}

// ============================================================================
// Output Types
// ============================================================================

export interface JSONSchemaProperty {
  type?: string | string[];
  description?: string;
  minimum?: number;
  maximum?: number;
  exclusiveMinimum?: number;
  exclusiveMaximum?: number;
  enum?: (string | number)[];
  format?: string;
  items?: JSONSchemaProperty;
  minItems?: number;
  maxItems?: number;
  minLength?: number;
  maxLength?: number;
  default?: unknown;
  'x-flatbuffer-type'?: string;
  'x-flatbuffer-field-id'?: number;
  'x-flatbuffer-deprecated'?: boolean;
  'x-xtce-unit'?: string;
  'x-xtce-unit-description'?: string;
  'x-xtce-encoding'?: string;
  'x-xtce-encoding-size'?: number;
  'x-xtce-calibrator'?: unknown;
  properties?: Record<string, JSONSchemaProperty>;
  required?: string[];
  additionalProperties?: boolean;
}

export interface JSONSchema {
  $schema: string;
  $id?: string;
  title?: string;
  description?: string;
  type: string;
  properties?: Record<string, JSONSchemaProperty>;
  definitions?: Record<string, JSONSchemaProperty>;
  required?: string[];
  additionalProperties?: boolean;
}

export interface FlatBufferField {
  name: string;
  type: string;
  id: number;
  defaultValue?: string | number | boolean;
  deprecated?: boolean;
  comment?: string;
}

export interface FlatBufferEnum {
  name: string;
  type: string;
  values: Array<{ name: string; value: number }>;
  comment?: string;
}

export interface FlatBufferTable {
  name: string;
  fields: FlatBufferField[];
  comment?: string;
}

export interface FlatBufferSchema {
  namespace?: string;
  fileIdentifier?: string;
  fileExtension?: string;
  rootType?: string;
  includes?: string[];
  enums: FlatBufferEnum[];
  tables: FlatBufferTable[];
}

export interface ConversionResult {
  jsonSchema: JSONSchema;
  flatBufferSchema: FlatBufferSchema;
  telemetryParameters: XTCEParameter[];
  commandArguments: XTCEArgument[];
  warnings: string[];
}

export interface ConversionOptions {
  namespace?: string;
  schemaId?: string;
  includeCommands?: boolean;
  includeTelemetry?: boolean;
  generateEnums?: boolean;
  fieldIdOffset?: number;
}
