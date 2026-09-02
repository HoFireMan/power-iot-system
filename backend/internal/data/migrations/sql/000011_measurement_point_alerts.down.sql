DO $$
BEGIN
    RAISE EXCEPTION 'MEASUREMENT_POINT_ALERTS_V1_01 DOWN is guarded; alert policy and lifecycle history are not destructively rolled back';
END
$$;
