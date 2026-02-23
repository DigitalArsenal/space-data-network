const SDN_CONTEXT_KEY = 'sdn-active-context'

const sdnContextBundle = {
  name: 'sdnContext',
  reducer (state = { activeContext: localStorage.getItem(SDN_CONTEXT_KEY) || 'sdn' }, action) {
    if (action.type === 'SDN_CONTEXT_SET') {
      return { ...state, activeContext: action.payload }
    }
    return state
  },
  doSetSdnContext (ctx) {
    return ({ dispatch }) => {
      localStorage.setItem(SDN_CONTEXT_KEY, ctx)
      dispatch({ type: 'SDN_CONTEXT_SET', payload: ctx })
    }
  },
  selectSdnActiveContext: state => state.sdnContext.activeContext,
  selectIsSdnContext: state => state.sdnContext.activeContext === 'sdn',
  selectIsIpfsContext: state => state.sdnContext.activeContext === 'ipfs'
}

export default sdnContextBundle
