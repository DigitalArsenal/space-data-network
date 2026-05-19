export function loadingMetricLabel(isLoading: boolean, formattedValue: string): string {
  return isLoading ? 'Loading' : formattedValue;
}
