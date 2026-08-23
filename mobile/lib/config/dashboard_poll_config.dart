const dashboardDefaultPollSeconds = 300;

const dashboardPollSecondsOverride = String.fromEnvironment(
  'POWER_IOT_DASHBOARD_POLL_SECONDS',
  defaultValue: '300',
);

Duration dashboardPollDuration({String? rawSeconds}) {
  final seconds = int.tryParse(rawSeconds ?? dashboardPollSecondsOverride);
  return Duration(
    seconds:
        seconds != null && seconds > 0 ? seconds : dashboardDefaultPollSeconds,
  );
}
