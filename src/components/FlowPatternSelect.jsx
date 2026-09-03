export function FlowPatternSelect({
  disabled,
  value,
  onChange,
}) {
  return (
    <select
      aria-label="Flow pattern"
      value={value}
      disabled={disabled}
      onChange={(event) => onChange(event.target.value)}
    >
      <option value="dashes">Dashes</option>
      <option value="dots">Dots</option>
      <option value="lumps">Lumps</option>
    </select>
  );
}
