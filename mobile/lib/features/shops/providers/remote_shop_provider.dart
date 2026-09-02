import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/core/network/remote_error.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/profile/presentation/providers/profile_provider.dart';
import 'package:power_iot_app/features/shops/data/repositories/shops_repository_impl.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';

class ShopsState {
  const ShopsState.loading()
      : status = RemoteStatus.loading,
        data = null,
        error = null,
        selectedShopId = null;
  const ShopsState.success(this.data, {this.selectedShopId})
      : status = RemoteStatus.success,
        error = null;
  const ShopsState.error(this.error)
      : status = RemoteStatus.error,
        data = null,
        selectedShopId = null;
  const ShopsState.unauthorized()
      : status = RemoteStatus.unauthorized,
        data = null,
        error = null,
        selectedShopId = null;

  final RemoteStatus status;
  final ShopsSnapshot? data;
  final Object? error;

  /// A view-only choice. It is never passed to a repository or API request.
  final String? selectedShopId;

  ShopsState withSelection(String? id) {
    if (status != RemoteStatus.success || data == null) return this;
    if (id != null && !data!.shops.any((shop) => shop.id == id)) return this;
    return ShopsState.success(data!, selectedShopId: id);
  }
}

final shopsRepositoryProvider = Provider<ShopsRepository>((ref) {
  return RemoteShopsRepository(ref.watch(authClientProvider));
});

/// Returns only a Shop already present in the current server-authorized
/// snapshot. `currentShopId` is a navigation preference, never authority.
String? authorizedShopId(ShopsState state) {
  if (state.status != RemoteStatus.success || state.data == null) return null;
  final requested = state.selectedShopId ?? state.data!.currentShopId;
  if (requested == null || requested.trim().isEmpty) return null;
  return state.data!.shops.any((shop) => shop.id == requested)
      ? requested
      : null;
}

class ShopsNotifier extends StateNotifier<ShopsState> {
  ShopsNotifier(this.repository, this.authClient)
      : super(const ShopsState.loading()) {
    _epoch = authClient.authEpoch;
    authClient.addAuthEpochListener(_authEpochChanged);
    authClient.session.addListener(_sessionChanged);
    load();
  }

  final ShopsRepository repository;
  final AuthenticatedHttpClient authClient;
  int _request = 0;
  late int _epoch;
  bool _loadAfterAuthentication = false;
  String? _selectedShopId;

  void _authEpochChanged(int epoch) {
    if (!mounted || epoch == _epoch) return;
    _epoch = epoch;
    _request++;
    _selectedShopId = null;
    _loadAfterAuthentication = !authClient.isLogoutInProgress;
    state = const ShopsState.loading();
  }

  void _sessionChanged() {
    if (!mounted || !authClient.session.isAuthenticated) {
      _loadAfterAuthentication = false;
      return;
    }
    if (_loadAfterAuthentication) {
      _loadAfterAuthentication = false;
      load();
    }
  }

  Future<void> load() async {
    final request = ++_request;
    final epoch = authClient.authEpoch;
    // Loading replaces the remote snapshot, but a local view choice can be
    // carried across the refresh and retained only if the new snapshot still
    // authorizes that Shop.
    final selectedShopId = _selectedShopId ?? state.selectedShopId;
    if (mounted) state = const ShopsState.loading();
    try {
      final shops = await repository.fetchShops();
      if (mounted &&
          request == _request &&
          authClient.isSessionCurrent(epoch)) {
        final selected = selectedShopId != null &&
                shops.shops.any((shop) => shop.id == selectedShopId)
            ? selectedShopId
            : null;
        _selectedShopId = selected;
        state = ShopsState.success(shops, selectedShopId: selected);
      }
    } catch (error) {
      if (!mounted ||
          request != _request ||
          !authClient.isSessionCurrent(epoch)) {
        return;
      }
      if (isUnauthorizedError(error)) {
        await authClient.session.clearIfCurrent(
          () => authClient.isSessionCurrent(epoch),
        );
        if (mounted &&
            request == _request &&
            authClient.isSessionCurrent(epoch)) {
          state = const ShopsState.unauthorized();
        }
      } else {
        state = ShopsState.error(error);
      }
    }
  }

  void selectShop(String shopId) {
    if (!mounted) return;
    final next = state.withSelection(shopId);
    if (next == state) return;
    _selectedShopId = next.selectedShopId;
    state = next;
  }

  void clearSelection() {
    if (!mounted) return;
    _selectedShopId = null;
    state = state.withSelection(null);
  }

  @override
  void dispose() {
    authClient.removeAuthEpochListener(_authEpochChanged);
    authClient.session.removeListener(_sessionChanged);
    super.dispose();
  }
}

final shopsProvider = StateNotifierProvider<ShopsNotifier, ShopsState>((ref) {
  return ShopsNotifier(
    ref.watch(shopsRepositoryProvider),
    ref.watch(authClientProvider),
  );
});
