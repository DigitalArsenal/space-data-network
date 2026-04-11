declare module '@spacedatanetwork/plugin-sdk' {
  export const MODULE_DELIVERY_PROTOCOL_ID: string;

  export interface ModuleDeliveryMessageEnvelope {
    schemaVersion?: number;
    type:
      | 'grant_request'
      | 'grant_challenge'
      | 'grant_proof'
      | 'grant_response'
      | 'error_response';
    payload: any;
  }

  export function encodeModuleDeliveryMessage(
    payload: ModuleDeliveryMessageEnvelope,
  ): Uint8Array;

  export function decodeModuleDeliveryMessage(
    bytes: Uint8Array,
  ): ModuleDeliveryMessageEnvelope;
}
