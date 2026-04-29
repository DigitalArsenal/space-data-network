const calc = (multiplier) => (value) => Math.round(value * multiplier)

const milliseconds = {
  seconds: calc(1e3),
  minutes: calc(6e4),
  hours: calc(36e5),
  days: calc(864e5),
  weeks: calc(6048e5),
  months: calc(26298e5),
  years: calc(315576e5)
}

export const { seconds, minutes, hours, days, weeks, months, years } = milliseconds
export default milliseconds
