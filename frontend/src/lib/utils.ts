export { cn } from "cn"

// Go's zero time marshals as year 1; it means "never" wherever a stamp is optional.
export const isZeroTime = (iso: string) => iso.startsWith("0001")
