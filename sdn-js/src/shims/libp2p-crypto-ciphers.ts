export const AES_GCM = {
  create() {
    return {
      encrypt(): never {
        throw new Error('AES-GCM key export is disabled in the SDN browser bundle');
      },
      decrypt(): never {
        throw new Error('AES-GCM key import is disabled in the SDN browser bundle');
      },
    };
  },
};
