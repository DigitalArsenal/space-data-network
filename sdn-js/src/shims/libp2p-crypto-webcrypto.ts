export default {
  get(): never {
    throw new Error('WebCrypto access is disabled in the SDN browser bundle');
  },
};
