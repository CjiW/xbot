/** Preset command stored in user_settings (key: preset_commands) */
export interface PresetCommand {
  id: string
  label: string
  icon: string
  content: string
  fill?: boolean  // true = fill editor instead of direct send
  sort: number
}
